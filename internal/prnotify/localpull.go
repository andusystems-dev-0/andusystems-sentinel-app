package prnotify

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// pullLocalCheckout fast-forwards the operator's working clone at
// <basePath>/<repo> from its forgejo remote so that a subsequent operator
// push cannot overwrite docs that sentinel just merged. The function is
// intentionally conservative:
//
//   - basePath empty       → feature disabled, no-op
//   - .git missing         → no checkout present, no-op
//   - working tree dirty   → skip with warning (do not touch in-progress work)
//   - HEAD not on main     → skip with info log (operator on a feature branch)
//   - non-fast-forward     → skip with warning (the operator and sentinel diverged)
//   - no forgejo or origin remote → skip
//
// Errors are logged but never returned as failures, since this is a
// best-effort safety net layered on top of the merge that already succeeded.
// Shells out to git to inherit the operator's credential helpers / SSH keys.
func pullLocalCheckout(ctx context.Context, basePath, repo string) {
	if basePath == "" {
		return
	}
	dir := filepath.Join(basePath, repo)

	if _, err := os.Stat(filepath.Join(dir, ".git")); os.IsNotExist(err) {
		slog.Debug("local checkout absent, skipping refresh", "repo", repo, "dir", dir)
		return
	} else if err != nil {
		slog.Warn("local checkout stat failed", "repo", repo, "dir", dir, "err", err)
		return
	}

	// Refuse to touch a dirty working tree.
	statusOut, err := runGit(ctx, dir, "status", "--porcelain")
	if err != nil {
		slog.Warn("local checkout status failed", "repo", repo, "dir", dir, "err", err)
		return
	}
	if strings.TrimSpace(statusOut) != "" {
		slog.Warn("local checkout has uncommitted changes; skipping refresh — run `git pull` manually before pushing",
			"repo", repo, "dir", dir)
		return
	}

	// Only refresh if the operator is sitting on the default branch.
	branchOut, err := runGit(ctx, dir, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		slog.Info("local checkout HEAD detached or unreadable, skipping refresh", "repo", repo, "err", err)
		return
	}
	branch := strings.TrimSpace(branchOut)
	if branch != "main" && branch != "master" {
		slog.Info("local checkout not on main/master, skipping refresh",
			"repo", repo, "branch", branch)
		return
	}

	remote := pickRemote(ctx, dir)
	if remote == "" {
		slog.Debug("local checkout has no forgejo/origin remote, skipping refresh", "repo", repo)
		return
	}

	if out, err := runGitCombined(ctx, dir, "fetch", remote, branch); err != nil {
		slog.Warn("local checkout fetch failed", "repo", repo, "remote", remote, "err", err, "output", out)
		return
	}

	if out, err := runGitCombined(ctx, dir, "merge", "--ff-only", remote+"/"+branch); err != nil {
		slog.Warn("local checkout fast-forward failed (operator and sentinel diverged); resolve manually",
			"repo", repo, "remote", remote, "branch", branch, "err", err, "output", out)
		return
	}

	slog.Info("local checkout fast-forwarded after merge",
		"repo", repo, "dir", dir, "remote", remote, "branch", branch)
}

// pickRemote returns the preferred remote name for fetching upstream changes:
// "forgejo" if it exists, then "origin", otherwise empty string.
func pickRemote(ctx context.Context, dir string) string {
	out, err := runGit(ctx, dir, "remote")
	if err != nil {
		return ""
	}
	var hasForgejo, hasOrigin bool
	for _, name := range strings.Split(strings.TrimSpace(out), "\n") {
		switch strings.TrimSpace(name) {
		case "forgejo":
			hasForgejo = true
		case "origin":
			hasOrigin = true
		}
	}
	if hasForgejo {
		return "forgejo"
	}
	if hasOrigin {
		return "origin"
	}
	return ""
}

func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

func runGitCombined(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}
