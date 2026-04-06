package sync

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/andusystems/sentinel/internal/config"
	"github.com/andusystems/sentinel/internal/store"
	"github.com/andusystems/sentinel/internal/types"
	"github.com/andusystems/sentinel/internal/worktree"
)

// Runner implements types.SyncRunner for Mode 3 incremental sync.
type Runner struct {
	cfg        *config.Config
	db         *store.DB
	wt         *worktree.Manager
	wtLock     types.ForgejoWorktreeLock
	pipeline   types.SanitizationPipeline
	discord    types.DiscordBot
	forge      types.ForgejoProvider
	approved   types.ApprovedValuesStore
}

// NewRunner creates a SyncRunner.
func NewRunner(
	cfg *config.Config,
	db *store.DB,
	wt *worktree.Manager,
	wtLock types.ForgejoWorktreeLock,
	pipeline types.SanitizationPipeline,
	discord types.DiscordBot,
	forge types.ForgejoProvider,
	approved types.ApprovedValuesStore,
) *Runner {
	return &Runner{
		cfg:      cfg,
		db:       db,
		wt:       wt,
		wtLock:   wtLock,
		pipeline: pipeline,
		discord:  discord,
		forge:    forge,
		approved: approved,
	}
}

// Sync runs Mode 3 incremental sync for a single repo.
func (r *Runner) Sync(ctx context.Context, repoName string) error {
	slog.Info("mode3 sync start", "repo", repoName)

	// Create SyncRun record.
	run := types.SyncRun{
		ID:        newID(),
		Repo:      repoName,
		Mode:      3,
		Status:    "running",
		StartedAt: time.Now(),
	}
	if err := r.db.SyncRuns.Create(ctx, run); err != nil {
		return fmt.Errorf("create sync run: %w", err)
	}

	// Pull latest Forgejo worktree.
	r.wtLock.Lock(repoName)
	if err := r.wt.EnsureForgejoWorktree(ctx, repoName); err != nil {
		r.wtLock.Unlock(repoName)
		run.Status = "failed"
		r.db.SyncRuns.Update(ctx, run)
		return err
	}

	lastSHA, err := r.db.SyncRuns.GetRepoSyncSHA(ctx, repoName)
	if err != nil {
		r.wtLock.Unlock(repoName)
		return err
	}

	worktreeDir := r.wt.ForgejoDir(repoName)
	diffs, currentSHA, err := ChangedFiles(worktreeDir, lastSHA)
	r.wtLock.Unlock(repoName)

	if err != nil {
		run.Status = "failed"
		r.db.SyncRuns.Update(ctx, run)
		return fmt.Errorf("changed files: %w", err)
	}

	// Process each changed file.
	for _, diff := range diffs {
		if r.isSkipPattern(diff.Filename) {
			continue
		}

		// Check for existing pending resolutions — skip re-sanitization.
		pending, _ := r.db.Resolutions.GetPendingForFile(ctx, repoName, diff.Filename)
		if len(pending) > 0 {
			// Copy current content to staging as-is.
			content, err := r.wt.ReadForgejoFile(ctx, repoName, diff.Filename)
			if err == nil {
				r.wt.WriteGitHubStaging(ctx, repoName, diff.Filename, content)
			}
			continue
		}

		content, err := r.wt.ReadForgejoFile(ctx, repoName, diff.Filename)
		if err != nil {
			slog.Warn("sync: read file failed", "file", diff.Filename, "err", err)
			continue
		}

		zones, _ := r.approved.GetSkipZones(ctx, repoName, content)

		result, err := r.pipeline.SanitizeFile(ctx, types.SanitizeFileOpts{
			Repo:      repoName,
			Filename:  diff.Filename,
			Content:   content,
			SkipZones: zones,
			SyncRunID: run.ID,
		})
		if err != nil {
			slog.Error("sync: sanitize failed", "file", diff.Filename, "err", err)
			continue
		}

		// Write sanitized content to GitHub staging.
		r.wt.WriteGitHubStaging(ctx, repoName, diff.Filename, result.SanitizedContent)

		// Process findings.
		for _, f := range result.Findings {
			f.SyncRunID = run.ID
			r.db.Findings.Create(ctx, f)

			if f.AutoRedacted {
				run.FindingsHigh++
			} else {
				// Medium/low: create pending resolution.
				resolution := types.PendingResolution{
					ID:                   newID(),
					Repo:                 repoName,
					Filename:             diff.Filename,
					FindingID:            f.ID,
					SyncRunID:            run.ID,
					SuggestedReplacement: f.SuggestedReplacement,
					Status:               types.StatusPending,
					DiscordChannelID:     r.cfg.Discord.FindingsChannelID,
				}

				r.db.Resolutions.Create(ctx, resolution)

				msgID, err := r.discord.PostFinding(ctx, resolution, f)
				if err != nil {
					slog.Error("sync: post finding failed", "finding", f.ID, "err", err)
					continue
				}

				// Update resolution with message ID.
				resolution.DiscordMessageID = msgID
				r.discord.SeedFindingReactions(ctx, r.cfg.Discord.FindingsChannelID, msgID)

				issueNum, _ := r.forge.CreateIssue(ctx, repoName, types.IssueOptions{
					Title: fmt.Sprintf("[Sentinel] %s finding in %s:%d", f.Category, filepath.Base(diff.Filename), f.LineNumber),
					Body:  fmt.Sprintf("Sanitization finding in `%s` at line %d.\n\nCategory: `%s`\nConfidence: `%s`\n\nSee Discord for resolution options.", diff.Filename, f.LineNumber, f.Category, f.Confidence),
				})
				if issueNum > 0 {
					r.db.Resolutions.SetIssueNumber(ctx, resolution.ID, issueNum)
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

	// Build the GitHub commit message from the original Forgejo commit messages.
	msgs, _ := CommitMessages(worktreeDir, lastSHA, currentSHA)
	commitMsg := FormatSyncCommitMessage(msgs)

	// Push all staged content to GitHub.
	commitSHA, pushErr := r.wt.PushAllStaging(ctx, repoName, commitMsg)
	if pushErr != nil {
		slog.Error("sync: push staging failed", "repo", repoName, "err", pushErr)
	}

	// Only update the baseline SHA if the push actually landed — otherwise
	// the reconciler (or next webhook) will correctly retry.
	if pushErr == nil {
		r.db.SyncRuns.SetRepoSyncSHA(ctx, repoName, currentSHA)
	}

	now := time.Now()
	run.CompletedAt = &now
	if run.FindingsMedium+run.FindingsLow > 0 {
		run.Status = "complete_with_pending"
	} else {
		run.Status = "complete"
	}
	r.db.SyncRuns.Update(ctx, run)

	commitLink := r.githubCommitURL(repoName, commitSHA)
	msg := fmt.Sprintf("✅ Sync complete for **%s**: %d files synced, %d pending findings",
		repoName, run.FilesSynced, run.FindingsMedium+run.FindingsLow)
	if commitLink != "" {
		msg += "\n" + commitLink
	}
	r.discord.PostChannelMessage(ctx, r.cfg.Discord.FindingsChannelID, msg)

	r.db.Actions.Log(ctx, "mode3_sync_complete", repoName, run.ID,
		fmt.Sprintf(`{"files_synced":%d,"findings_high":%d,"findings_medium":%d,"findings_low":%d}`,
			run.FilesSynced, run.FindingsHigh, run.FindingsMedium, run.FindingsLow))

	slog.Info("mode3 sync complete", "repo", repoName, "files", run.FilesSynced)
	return nil
}

func (r *Runner) isSkipPattern(filename string) bool {
	for _, pattern := range r.cfg.Sanitize.SkipPatterns {
		matched, _ := filepath.Match(pattern, filename)
		if matched {
			return true
		}
		// Also check basename.
		matched, _ = filepath.Match(pattern, filepath.Base(filename))
		if matched {
			return true
		}
	}
	return false
}

// githubCommitURL returns the GitHub web URL for a commit, or "" if the repo
// config or SHA is not available.
func (r *Runner) githubCommitURL(repoName, sha string) string {
	if sha == "" {
		return ""
	}
	for _, repo := range r.cfg.Repos {
		if repo.Name == repoName && repo.GitHubPath != "" {
			return fmt.Sprintf("https://github.com/%s/commit/%s", repo.GitHubPath, sha)
		}
	}
	return ""
}

func newID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}
