package worktree

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	gogit "github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport/http"

	"github.com/andusystems/sentinel/internal/config"
)

// PushStagingFile commits and pushes a single file from the GitHub staging
// worktree to the GitHub mirror repo. Returns the commit SHA on success.
// Caller MUST hold FileMutexRegistry.Lock(repo, filename).
func (m *Manager) PushStagingFile(ctx context.Context, repo, filename, commitMsg string) (string, error) {
	return m.pushFiles(ctx, repo, commitMsg, []string{filename}, false)
}

// PushAllStaging commits and pushes all staged changes in the GitHub staging
// worktree as a single squashed commit. Returns the commit SHA on success.
//
// WARNING: this uses go-git's AddGlob(".") which removes index entries for
// any file not present on disk. Because the staging worktree only ever has
// the freshly-written file subset (not all of origin/main), AddGlob(".")
// will silently delete every other file from the GitHub mirror. Sync mode 3
// must use PushStagingFiles instead. AddGlob is only safe in Mode 4 initial
// migration where the staging worktree is a complete copy of the source.
func (m *Manager) PushAllStaging(ctx context.Context, repo, commitMsg string) (string, error) {
	return m.pushFiles(ctx, repo, commitMsg, nil, false) // nil = add all, no force
}

// PushStagingFiles commits and pushes only the named files from the GitHub
// staging worktree as a single commit. The per-file `wt.Add` path is taken,
// which leaves index entries for other files (inherited from origin/main
// after fast-forward) untouched. Use this from Mode 3 sync where staging
// only contains the changed files, not the full tree.
func (m *Manager) PushStagingFiles(ctx context.Context, repo, commitMsg string, filenames []string) (string, error) {
	return m.pushFiles(ctx, repo, commitMsg, filenames, false)
}

// PushAllStagingInitial commits all staged content as a single commit and
// force-pushes it to the remote "main" branch, replacing any prior history.
// Used by Mode 4 initial migration so GitHub receives exactly one clean commit.
func (m *Manager) PushAllStagingInitial(ctx context.Context, repo, commitMsg string) (string, error) {
	return m.pushFiles(ctx, repo, commitMsg, nil, true)
}

func (m *Manager) pushFiles(ctx context.Context, repo, commitMsg string, filenames []string, force bool) (string, error) {
	dir := m.githubDirFor(repo)

	r, err := m.ensureGitHubRepo(ctx, repo, dir)
	if err != nil {
		return "", err
	}

	// Fetch remote so we know where origin/main is. If the remote is ahead
	// (e.g. previous migration, direct push), a non-force push would fail
	// with "non-fast-forward". Fetching lets us detect and handle this.
	repoConfig := m.repoConfig(repo)
	if repoConfig == nil {
		return "", fmt.Errorf("repo %q not found in config", repo)
	}
	fetchURL := fmt.Sprintf("https://github.com/%s.git", repoConfig.GitHubPath)
	fetchErr := r.FetchContext(ctx, &gogit.FetchOptions{
		RemoteName: "origin",
		Auth: &http.BasicAuth{
			Username: m.cfg.Sentinel.GitHubUsername,
			Password: m.cfg.GitHub.Token,
		},
		RemoteURL: fetchURL,
	})
	if fetchErr != nil && fetchErr != gogit.NoErrAlreadyUpToDate {
		// Non-fatal: if fetch fails (e.g. empty remote), proceed anyway.
		slog.Debug("push: fetch origin failed (may be empty remote)", "repo", repo, "err", fetchErr)
	}

	// If remote main is ahead of local, fast-forward local HEAD so our new
	// commit builds on the latest remote state. This keeps the push linear.
	if !force {
		if ffErr := m.fastForwardToRemote(r, repo); ffErr != nil {
			slog.Debug("push: fast-forward skipped", "repo", repo, "err", ffErr)
		}
	}

	wt, err := r.Worktree()
	if err != nil {
		return "", err
	}

	if filenames == nil {
		// Add all changes.
		if err := wt.AddGlob("."); err != nil {
			return "", fmt.Errorf("git add all in %s: %w", repo, err)
		}
	} else {
		for _, f := range filenames {
			rel, err := filepath.Rel(dir, filepath.Join(dir, f))
			if err != nil {
				return "", err
			}
			if _, err := wt.Add(rel); err != nil {
				return "", fmt.Errorf("git add %s in %s: %w", f, repo, err)
			}
		}
	}

	// Use the GitHub identity for mirror commits so they show as the
	// operator's commits, not the sentinel service account.
	name := m.cfg.GitHub.GitName
	email := m.cfg.GitHub.GitEmail
	if name == "" {
		name = m.cfg.Sentinel.GitName
	}
	if email == "" {
		email = m.cfg.Sentinel.GitEmail
	}
	sig := &object.Signature{
		Name:  name,
		Email: email,
		When:  time.Now(),
	}

	hash, err := wt.Commit(commitMsg, &gogit.CommitOptions{Author: sig, Committer: sig})
	if err != nil {
		return "", fmt.Errorf("commit staging for %s: %w", repo, err)
	}

	pushURL := fmt.Sprintf("https://github.com/%s.git", repoConfig.GitHubPath)
	pushOpts := &gogit.PushOptions{
		RemoteName: "origin",
		Auth: &http.BasicAuth{
			Username: m.cfg.Sentinel.GitHubUsername,
			Password: m.cfg.GitHub.Token,
		},
		RemoteURL: pushURL,
	}
	if force {
		// Force-push local main to remote main so initial migrations overwrite
		// any prior GitHub history. Use fully-qualified refspec — go-git does
		// not always resolve "HEAD" as a push source.
		pushOpts.Force = true
		pushOpts.RefSpecs = []gitconfig.RefSpec{
			gitconfig.RefSpec("+refs/heads/main:refs/heads/main"),
		}
	}
	err = r.PushContext(ctx, pushOpts)
	if err != nil && err != gogit.NoErrAlreadyUpToDate {
		return "", fmt.Errorf("push github staging for %s: %w", repo, err)
	}
	if err == gogit.NoErrAlreadyUpToDate && force {
		// A force-push from Mode 4 should never be up-to-date — this usually
		// means the refspec failed to match any local ref, so nothing was
		// actually sent. Surface as an error.
		return "", fmt.Errorf("push github staging for %s: no-op (remote unchanged) — check refspec/branch", repo)
	}
	return hash.String(), nil
}

// ensureGitHubRepo opens or initialises the GitHub staging git repo at dir,
// and guarantees an "origin" remote pointing at the configured GitHub path.
func (m *Manager) ensureGitHubRepo(ctx context.Context, repo, dir string) (*gogit.Repository, error) {
	repoConfig := m.repoConfig(repo)
	if repoConfig == nil {
		return nil, fmt.Errorf("repo %q not found in config", repo)
	}
	remoteURL := fmt.Sprintf("https://github.com/%s.git", repoConfig.GitHubPath)

	r, err := gogit.PlainOpen(dir)
	if err != nil {
		// Init a new repo.
		r, err = gogit.PlainInit(dir, false)
		if err != nil {
			return nil, fmt.Errorf("git init github staging %s: %w", repo, err)
		}
		// Point HEAD at refs/heads/main so the first commit creates the
		// "main" branch directly (go-git's PlainInit defaults to "master").
		headRef := plumbing.NewSymbolicReference(
			plumbing.HEAD, plumbing.ReferenceName("refs/heads/main"))
		if err := r.Storer.SetReference(headRef); err != nil {
			return nil, fmt.Errorf("set HEAD to main for %s: %w", repo, err)
		}
	}

	// Ensure origin remote is configured (and points at the expected URL).
	existing, err := r.Remote("origin")
	if err != nil {
		// Remote missing: create it.
		if _, err := r.CreateRemote(&gitconfig.RemoteConfig{
			Name: "origin",
			URLs: []string{remoteURL},
		}); err != nil {
			return nil, fmt.Errorf("create origin remote for %s: %w", repo, err)
		}
	} else if len(existing.Config().URLs) == 0 || existing.Config().URLs[0] != remoteURL {
		// Remote present but URL drifted (e.g. stale org): replace it.
		if err := r.DeleteRemote("origin"); err != nil {
			return nil, fmt.Errorf("delete stale origin remote for %s: %w", repo, err)
		}
		if _, err := r.CreateRemote(&gitconfig.RemoteConfig{
			Name: "origin",
			URLs: []string{remoteURL},
		}); err != nil {
			return nil, fmt.Errorf("recreate origin remote for %s: %w", repo, err)
		}
	}

	_ = ctx // context available for future remote operations
	return r, nil
}

// ResolutionCommitMsg formats the commit message for a single resolved finding.
// Format: chore(sync): sentinel resolved <category> in <basename>:<line>
func ResolutionCommitMsg(category, filename string, lineNumber int) string {
	return fmt.Sprintf("chore(sync): sentinel resolved %s in %s:%d",
		category, filepath.Base(filename), lineNumber)
}

// fastForwardToRemote fast-forwards the local main branch to match
// origin/main. This ensures the next commit builds on the latest remote
// state so the push is a fast-forward. Working tree files are preserved
// (git reset --mixed keeps the worktree intact).
func (m *Manager) fastForwardToRemote(r *gogit.Repository, repo string) error {
	remoteRef, err := r.Reference(plumbing.NewRemoteReferenceName("origin", "main"), true)
	if err != nil {
		return fmt.Errorf("resolve origin/main: %w", err)
	}

	localRef, err := r.Head()
	if err != nil {
		// Local has no HEAD: PlainInit was just called on a previously
		// missing/empty staging dir, so refs/heads/main is unborn. Point
		// it at origin/main so the next commit lands as a fast-forward
		// child instead of an orphan branch (which the GitHub side would
		// reject as non-fast-forward). Fall through to the mixed reset
		// below so the index also reflects origin/main's tree, allowing
		// the next AddGlob to stage the worktree as a delta against the
		// remote rather than as a from-scratch tree.
		branchRef := plumbing.NewHashReference(
			plumbing.NewBranchReferenceName("main"),
			remoteRef.Hash())
		if err := r.Storer.SetReference(branchRef); err != nil {
			return fmt.Errorf("set unborn local main to origin/main: %w", err)
		}
		slog.Info("push: adopted origin/main as new local main",
			"repo", repo, "sha", remoteRef.Hash().String()[:12])
		// Fall through to mixed reset so the index matches origin/main.
	} else if localRef.Hash() == remoteRef.Hash() {
		return nil // already up-to-date
	}

	// Move local HEAD + index to origin/main, but LEAVE the working tree
	// untouched. The freshly-written staging files from WriteGitHubStaging
	// are already on disk and must survive into the subsequent AddGlob(".").
	//
	// HardReset is wrong here: if a previous failed push left a local commit
	// that tracked a file (e.g. .github/workflows/*.yml when the PAT lacked
	// `workflow` scope), git considers that file tracked at old HEAD. A
	// hard-reset to a remote that doesn't have the file deletes it from the
	// worktree even though we just wrote fresh content there a few lines
	// earlier in the runner. AddGlob then sees nothing to stage and we
	// commit/push an EMPTY commit with the right subject — silently dropping
	// the file. MixedReset preserves the worktree, so the next AddGlob
	// re-stages everything we wrote.
	wt, err := r.Worktree()
	if err != nil {
		return err
	}
	if err := wt.Reset(&gogit.ResetOptions{
		Commit: remoteRef.Hash(),
		Mode:   gogit.MixedReset,
	}); err != nil {
		return fmt.Errorf("reset to origin/main: %w", err)
	}

	slog.Info("push: fast-forwarded local to origin/main",
		"repo", repo, "sha", remoteRef.Hash().String()[:12])
	return nil
}

// config is a convenience accessor for the config used in tests.
func (m *Manager) Config() *config.Config { return m.cfg }
