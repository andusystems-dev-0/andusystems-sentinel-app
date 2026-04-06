package worktree

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport/http"

	"github.com/andusystems/sentinel/internal/config"
)

// Manager implements types.WorktreeManager.
// It owns:
//   - The Forgejo worktree: {basePath}/forgejo/{repo}
//   - The GitHub staging worktree: {basePath}/github/{repo}
type Manager struct {
	cfg      *config.Config
	lock     *forgejoWorktreeLock
	fileLock *fileMutexRegistry
}

// NewManager constructs a WorktreeManager.
func NewManager(cfg *config.Config, lock *forgejoWorktreeLock, fileLock *fileMutexRegistry) *Manager {
	return &Manager{cfg: cfg, lock: lock, fileLock: fileLock}
}

// forgejoDirFor returns the Forgejo worktree path for a repo.
func (m *Manager) forgejoDirFor(repo string) string {
	return filepath.Join(m.cfg.Worktree.BasePath, "forgejo", repo)
}

// githubDirFor returns the GitHub staging path for a repo.
func (m *Manager) githubDirFor(repo string) string {
	return filepath.Join(m.cfg.Worktree.BasePath, "github", repo)
}

// EnsureForgejoWorktree clones the Forgejo repo if not present, or pulls if it is.
// Caller should hold ForgejoWorktreeLock.Lock(repo).
func (m *Manager) EnsureForgejoWorktree(ctx context.Context, repo string) error {
	dir := m.forgejoDirFor(repo)

	repoConfig := m.repoConfig(repo)
	if repoConfig == nil {
		return fmt.Errorf("repo %q not found in config", repo)
	}

	cloneURL := fmt.Sprintf("%s/%s.git", m.cfg.Forgejo.BaseURL, repoConfig.ForgejoPath)
	auth := &http.BasicAuth{
		Username: m.cfg.Sentinel.ForgejoUsername,
		Password: m.cfg.Forgejo.SentinelToken,
	}

	if _, err := os.Stat(filepath.Join(dir, ".git")); os.IsNotExist(err) {
		// Clone fresh.
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("mkdir forgejo worktree %s: %w", dir, err)
		}
		_, err := gogit.PlainCloneContext(ctx, dir, false, &gogit.CloneOptions{
			URL:  cloneURL,
			Auth: auth,
		})
		if err != nil {
			return fmt.Errorf("clone %s: %w", cloneURL, err)
		}
		return nil
	}

	// Existing worktree: fetch and hard-reset the default branch to origin.
	// Sentinel owns this worktree exclusively — any divergence from the remote
	// is drift (stale local merges, rebased upstream) and should be discarded.
	// Task/sentinel branches off main are preserved (not touched by reset).
	r, err := gogit.PlainOpen(dir)
	if err != nil {
		return fmt.Errorf("open forgejo repo %s: %w", dir, err)
	}
	wt, err := r.Worktree()
	if err != nil {
		return err
	}

	if err := r.FetchContext(ctx, &gogit.FetchOptions{
		Auth:  auth,
		Prune: true,
	}); err != nil && err != gogit.NoErrAlreadyUpToDate {
		return fmt.Errorf("fetch %s: %w", repo, err)
	}

	defaultBranch, err := resolveOriginDefaultBranch(r)
	if err != nil {
		return fmt.Errorf("resolve default branch for %s: %w", repo, err)
	}
	remoteRef, err := r.Reference(
		plumbing.ReferenceName("refs/remotes/origin/"+defaultBranch), true)
	if err != nil {
		return fmt.Errorf("resolve origin/%s for %s: %w", defaultBranch, repo, err)
	}

	// Checkout (or create) the local default branch pointing at origin's SHA.
	localRef := plumbing.ReferenceName("refs/heads/" + defaultBranch)
	if err := wt.Checkout(&gogit.CheckoutOptions{
		Branch: localRef,
		Create: false,
		Force:  true,
	}); err != nil {
		// Branch may not exist locally yet — create it from the remote SHA.
		if err := wt.Checkout(&gogit.CheckoutOptions{
			Branch: localRef,
			Hash:   remoteRef.Hash(),
			Create: true,
			Force:  true,
		}); err != nil {
			return fmt.Errorf("checkout %s for %s: %w", defaultBranch, repo, err)
		}
	}

	// Hard-reset the default branch to match origin.
	if err := wt.Reset(&gogit.ResetOptions{
		Commit: remoteRef.Hash(),
		Mode:   gogit.HardReset,
	}); err != nil {
		return fmt.Errorf("hard reset %s to origin/%s: %w", repo, defaultBranch, err)
	}
	return nil
}

// resolveOriginDefaultBranch returns the short name (e.g. "main") of the
// default branch as advertised by the origin remote. Falls back to "main".
func resolveOriginDefaultBranch(r *gogit.Repository) (string, error) {
	// refs/remotes/origin/HEAD is a symbolic ref pointing at the real default.
	head, err := r.Reference(plumbing.ReferenceName("refs/remotes/origin/HEAD"), false)
	if err == nil && head.Type() == plumbing.SymbolicReference {
		target := head.Target().String() // e.g. "refs/remotes/origin/main"
		const prefix = "refs/remotes/origin/"
		if len(target) > len(prefix) && target[:len(prefix)] == prefix {
			return target[len(prefix):], nil
		}
	}
	// Fallback: assume main.
	if _, err := r.Reference(plumbing.ReferenceName("refs/remotes/origin/main"), true); err == nil {
		return "main", nil
	}
	if _, err := r.Reference(plumbing.ReferenceName("refs/remotes/origin/master"), true); err == nil {
		return "master", nil
	}
	return "", fmt.Errorf("no origin/main or origin/master ref found")
}

// ReadForgejoFile reads a file from the Forgejo worktree.
func (m *Manager) ReadForgejoFile(_ context.Context, repo, filename string) ([]byte, error) {
	path := filepath.Join(m.forgejoDirFor(repo), filename)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read forgejo file %s/%s: %w", repo, filename, err)
	}
	return data, nil
}

// WriteGitHubStaging writes sanitized content to the GitHub staging worktree.
// Creates parent directories as needed.
func (m *Manager) WriteGitHubStaging(_ context.Context, repo, filename string, content []byte) error {
	path := filepath.Join(m.githubDirFor(repo), filename)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir github staging: %w", err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return fmt.Errorf("write github staging %s/%s: %w", repo, filename, err)
	}
	return nil
}

// ReadGitHubStaging reads a file from the GitHub staging worktree.
func (m *Manager) ReadGitHubStaging(_ context.Context, repo, filename string) ([]byte, error) {
	path := filepath.Join(m.githubDirFor(repo), filename)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read github staging %s/%s: %w", repo, filename, err)
	}
	return data, nil
}

// ResolveTag resolves the token_index tag in the GitHub staging file with finalValue.
// Caller MUST hold FileMutexRegistry.Lock(repo, filename).
func (m *Manager) ResolveTag(_ context.Context, repo, filename string, tokenIndex, resolvedCount int, finalValue string) error {
	path := filepath.Join(m.githubDirFor(repo), filename)

	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read staging file for resolve: %w", err)
	}

	modified, err := resolveTokenIndex(content, tokenIndex, resolvedCount, finalValue)
	if err != nil {
		return fmt.Errorf("resolve token_index %d in %s/%s: %w", tokenIndex, repo, filename, err)
	}

	if err := os.WriteFile(path, modified, 0o644); err != nil {
		return fmt.Errorf("write resolved staging file: %w", err)
	}
	return nil
}

// SentinelTag returns the tag string for a category using config.Sanitize.CategoryReasons.
func (m *Manager) SentinelTag(category string) string {
	reason, ok := m.cfg.Sanitize.CategoryReasons[category]
	if !ok {
		reason = "sensitive value detected"
	}
	return fmt.Sprintf("<REMOVED BY SENTINEL BOT: %s — %s>", category, reason)
}

// repoConfig finds the RepoConfig for a repo by name.
func (m *Manager) repoConfig(repo string) *config.RepoConfig {
	for i := range m.cfg.Repos {
		if m.cfg.Repos[i].Name == repo {
			return &m.cfg.Repos[i]
		}
	}
	return nil
}

// ForgejoDir returns the Forgejo worktree directory path for a repo (used by pipeline/sync).
func (m *Manager) ForgejoDir(repo string) string {
	return m.forgejoDirFor(repo)
}

// ResetGitHubStaging removes the entire GitHub staging directory for a repo,
// discarding any accumulated .git history and working-tree contents. Used by
// Mode 4 initial migration to guarantee a single clean commit on GitHub.
// Subsequent operations will re-init the dir via ensureGitHubRepo.
func (m *Manager) ResetGitHubStaging(_ context.Context, repo string) error {
	dir := m.githubDirFor(repo)
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("reset github staging %s: %w", repo, err)
	}
	return nil
}

// RemoveFromGitHubStaging deletes a file from the GitHub staging worktree.
// Used to ensure skip-pattern files (e.g. .[AI_ASSISTANT]/, [AI_ASSISTANT].md) are never committed.
// Silently succeeds if the file does not exist.
func (m *Manager) RemoveFromGitHubStaging(_ context.Context, repo, filename string) error {
	path := filepath.Join(m.githubDirFor(repo), filename)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove github staging %s/%s: %w", repo, filename, err)
	}
	// Remove parent directories if empty (best-effort).
	dir := filepath.Dir(path)
	for dir != m.githubDirFor(repo) {
		if err := os.Remove(dir); err != nil {
			break // not empty or other error; stop climbing
		}
		dir = filepath.Dir(dir)
	}
	return nil
}
