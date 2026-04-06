package migration

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/andusystems/sentinel/internal/config"
	"github.com/andusystems/sentinel/internal/store"
	"github.com/andusystems/sentinel/internal/types"
	"github.com/andusystems/sentinel/internal/worktree"
)

// Manager implements types.MigrationManager for Mode 4 initial migration.
type Manager struct {
	cfg      *config.Config
	db       *store.DB
	wt       *worktree.Manager
	wtLock   types.ForgejoWorktreeLock
	pipeline types.SanitizationPipeline
	discord  types.DiscordBot
	forge    types.ForgejoProvider
	github   types.GitHubProvider
	approved types.ApprovedValuesStore
}

// NewManager creates a MigrationManager.
func NewManager(
	cfg *config.Config,
	db *store.DB,
	wt *worktree.Manager,
	wtLock types.ForgejoWorktreeLock,
	pipeline types.SanitizationPipeline,
	discord types.DiscordBot,
	forge types.ForgejoProvider,
	github types.GitHubProvider,
	approved types.ApprovedValuesStore,
) *Manager {
	return &Manager{
		cfg:      cfg,
		db:       db,
		wt:       wt,
		wtLock:   wtLock,
		pipeline: pipeline,
		discord:  discord,
		forge:    forge,
		github:   github,
		approved: approved,
	}
}

// Migrate runs Mode 4 full-repo migration for repoName.
func (m *Manager) Migrate(ctx context.Context, repoName string, force bool) error {
	slog.Info("mode4 migration start", "repo", repoName, "force", force)

	// Pre-checks.
	status, err := m.db.Reviews.GetMigrationStatus(ctx, repoName)
	if err != nil {
		return err
	}
	if status == "complete" && !force {
		return fmt.Errorf("repo %q already migrated. Use --force to re-migrate", repoName)
	}

	// Resolve GitHub path + description from config.
	var githubPath, description string
	for _, r := range m.cfg.Repos {
		if r.Name == repoName {
			githubPath = r.GitHubPath
			description = r.Description
			break
		}
	}
	if githubPath == "" {
		return fmt.Errorf("repo %q not found in config", repoName)
	}

	// GitHub preflight: validate token, create the mirror repo if it doesn't
	// exist, and sync its description. This runs before sanitization so auth
	// failures surface immediately rather than after all the expensive
	// processing is done.
	if err := m.github.EnsureRepo(ctx, githubPath, description); err != nil {
		m.discord.PostChannelMessage(ctx, m.cfg.Discord.CommandChannelID,
			fmt.Sprintf(
				"❌ Migration preflight failed for **%s**\n\n"+
					"Cannot access or create GitHub mirror repo `%s`.\n"+
					"Error: `%s`\n\n"+
					"Check that `GITHUB_TOKEN` is set, has `repo` scope, and the `%s` org exists.\n"+
					"Fix the token and re-run the migrate command.",
				repoName, githubPath, err.Error(), m.cfg.GitHub.Org,
			),
		)
		return fmt.Errorf("github preflight for %s: %w", repoName, err)
	}
	slog.Info("mode4 migration: github repo ready", "repo", repoName, "github_path", githubPath)

	if force {
		// Require operator confirmation.
		confirmed, err := ConfirmForce(ctx, repoName, m.db, m.discord,
			m.cfg.Discord.CommandChannelID, m.cfg.Allowlist.ConfirmationTTLMinutes)
		if err != nil {
			return fmt.Errorf("force confirmation: %w", err)
		}
		if !confirmed {
			return fmt.Errorf("force migration not confirmed")
		}
	}

	// Pull latest.
	m.wtLock.Lock(repoName)
	if err := m.wt.EnsureForgejoWorktree(ctx, repoName); err != nil {
		m.wtLock.Unlock(repoName)
		return fmt.Errorf("ensure worktree: %w", err)
	}

	// Walk all files in HEAD.
	worktreeDir := m.wt.ForgejoDir(repoName)
	repo, err := gogit.PlainOpen(worktreeDir)
	if err != nil {
		m.wtLock.Unlock(repoName)
		return err
	}

	head, err := repo.Head()
	if err != nil {
		m.wtLock.Unlock(repoName)
		return err
	}
	headSHA := head.Hash().String()

	headCommit, err := repo.CommitObject(head.Hash())
	if err != nil {
		m.wtLock.Unlock(repoName)
		return err
	}
	headTree, err := headCommit.Tree()
	if err != nil {
		m.wtLock.Unlock(repoName)
		return err
	}

	// Collect all file names.
	var filenames []string
	headTree.Files().ForEach(func(f *object.File) error {
		filenames = append(filenames, f.Name)
		return nil
	})
	m.wtLock.Unlock(repoName)

	// Wipe the GitHub staging directory so this migration produces a single
	// clean commit on the remote (no leaked Forgejo history, no accumulated
	// sentinel commits from prior runs). Subsequent Mode 3 syncs will build
	// incremental history on top of this base commit.
	if err := m.wt.ResetGitHubStaging(ctx, repoName); err != nil {
		return fmt.Errorf("reset github staging: %w", err)
	}

	// Start migration.
	m.db.Reviews.StartMigration(ctx, repoName)
	m.db.Actions.Log(ctx, "mode4_migration_start", repoName, "", fmt.Sprintf(`{"force":%v,"files":%d}`, force, len(filenames)))

	runID := newID()
	run := types.SyncRun{
		ID:        runID,
		Repo:      repoName,
		Mode:      4,
		Status:    "running",
		StartedAt: time.Now(),
	}
	m.db.SyncRuns.Create(ctx, run)

	slog.Info("mode4 migration: processing files", "repo", repoName, "total", len(filenames))

	// Process each file sequentially.
	for i, filename := range filenames {
		if m.isSkipPattern(filename) {
			slog.Info("mode4 migration: skipping", "file", filename, "n", fmt.Sprintf("%d/%d", i+1, len(filenames)))
			continue
		}

		slog.Info("mode4 migration: sanitizing", "file", filename, "n", fmt.Sprintf("%d/%d", i+1, len(filenames)))

		content, err := m.wt.ReadForgejoFile(ctx, repoName, filename)
		if err != nil {
			slog.Warn("migration: read file failed", "file", filename, "err", err)
			continue
		}

		zones, _ := m.approved.GetSkipZones(ctx, repoName, content)

		result, err := m.pipeline.SanitizeFile(ctx, types.SanitizeFileOpts{
			Repo:      repoName,
			Filename:  filename,
			Content:   content,
			SkipZones: zones,
			SyncRunID: runID,
		})
		if err != nil {
			slog.Error("migration: sanitize failed", "file", filename, "err", err)
			continue
		}

		m.wt.WriteGitHubStaging(ctx, repoName, filename, result.SanitizedContent)

		for _, f := range result.Findings {
			f.SyncRunID = runID
			m.db.Findings.Create(ctx, f)

			if f.AutoRedacted {
				run.FindingsHigh++
			} else {
				resolution := types.PendingResolution{
					ID:                   newID(),
					Repo:                 repoName,
					Filename:             filename,
					FindingID:            f.ID,
					SyncRunID:            runID,
					SuggestedReplacement: f.SuggestedReplacement,
					Status:               types.StatusPending,
					DiscordChannelID:     m.cfg.Discord.FindingsChannelID,
				}
				m.db.Resolutions.Create(ctx, resolution)

				msgID, err := m.discord.PostFinding(ctx, resolution, f)
				if err == nil {
					m.discord.SeedFindingReactions(ctx, m.cfg.Discord.FindingsChannelID, msgID)
					issueNum, _ := m.forge.CreateIssue(ctx, repoName, types.IssueOptions{
						Title: fmt.Sprintf("[Sentinel] %s finding in %s:%d", f.Category, filepath.Base(filename), f.LineNumber),
						Body:  fmt.Sprintf("Sanitization finding during Mode 4 migration.\n\nFile: `%s`\nLine: %d\nCategory: `%s`\nConfidence: `%s`", filename, f.LineNumber, f.Category, f.Confidence),
					})
					if issueNum > 0 {
						m.db.Resolutions.SetIssueNumber(ctx, resolution.ID, issueNum)
					}
				}

				if f.Confidence == "medium" {
					run.FindingsMedium++
				} else {
					run.FindingsLow++
				}
				run.FilesWithPending++
			}
		}
		run.FilesSynced++
	}

	// Remove skip-pattern files from staging before committing.
	// These may persist from a previous run; we must never push them to GitHub.
	for _, filename := range filenames {
		if m.isSkipPattern(filename) {
			if err := m.wt.RemoveFromGitHubStaging(ctx, repoName, filename); err != nil {
				slog.Warn("migration: could not remove skip-pattern file from staging", "file", filename, "err", err)
			}
		}
	}

	// Single orphan commit force-pushed to main — overwrites any prior
	// GitHub history so the initial migration is always one clean commit.
	if sha, err := m.wt.PushAllStagingInitial(ctx, repoName, fmt.Sprintf("chore(migrate): initial sentinel migration of %s", repoName)); err != nil {
		slog.Error("migration: push staging failed", "repo", repoName, "err", err)
	} else {
		slog.Info("migration: push staging ok", "repo", repoName, "sha", sha)
	}

	// Update state.
	m.db.SyncRuns.SetRepoSyncSHA(ctx, repoName, headSHA)
	m.db.Reviews.CompleteMigration(ctx, repoName, headSHA)

	now := time.Now()
	run.CompletedAt = &now
	run.Status = "complete"
	m.db.SyncRuns.Update(ctx, run)

	m.discord.PostChannelMessage(ctx, m.cfg.Discord.CommandChannelID,
		fmt.Sprintf(
			"✅ Migration complete: **%s**\n"+
				"  Files migrated: %d\n"+
				"  High-confidence auto-redacted: %d\n"+
				"  Pending operator review: %d\n"+
				"  Forgejo HEAD: `%s`",
			repoName, run.FilesSynced, run.FindingsHigh,
			run.FindingsMedium+run.FindingsLow, headSHA,
		),
	)

	m.db.Actions.Log(ctx, "mode4_migration_complete", repoName, runID,
		fmt.Sprintf(`{"files":%d,"high":%d,"medium":%d,"low":%d}`,
			run.FilesSynced, run.FindingsHigh, run.FindingsMedium, run.FindingsLow))

	slog.Info("mode4 migration complete", "repo", repoName, "files", run.FilesSynced)
	return nil
}

// Status returns the current migration state for a repo.
func (m *Manager) Status(_ context.Context, repoName string) (*types.MigrationState, error) {
	return &types.MigrationState{Repo: repoName}, nil
}

func (m *Manager) isSkipPattern(filename string) bool {
	for _, pattern := range m.cfg.Sanitize.SkipPatterns {
		// Handle directory glob patterns like ".[AI_ASSISTANT]/**"
		if strings.HasSuffix(pattern, "/**") {
			dir := strings.TrimSuffix(pattern, "/**")
			if strings.HasPrefix(filename, dir+"/") || filename == dir {
				return true
			}
			continue
		}
		if matched, _ := filepath.Match(pattern, filename); matched {
			return true
		}
		if matched, _ := filepath.Match(pattern, filepath.Base(filename)); matched {
			return true
		}
	}
	return false
}
