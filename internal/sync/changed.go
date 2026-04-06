// Package sync implements Mode 3 incremental Forgejo→GitHub sync.
// Must-NOT open Forgejo PRs.
package sync

import (
	"fmt"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/andusystems/sentinel/internal/types"
)

// CommitMessages returns the commit messages between fromSHA (exclusive) and
// toSHA (inclusive) in reverse-chronological order. Used to build meaningful
// GitHub mirror commit messages from the original Forgejo history.
func CommitMessages(dir, fromSHA, toSHA string) ([]string, error) {
	repo, err := gogit.PlainOpen(dir)
	if err != nil {
		return nil, err
	}
	toHash := plumbing.NewHash(toSHA)
	iter, err := repo.Log(&gogit.LogOptions{From: toHash})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var msgs []string
	err = iter.ForEach(func(c *object.Commit) error {
		if c.Hash.String() == fromSHA {
			return fmt.Errorf("stop") // sentinel; not a real error
		}
		// Use first line only (subject).
		subject := c.Message
		if idx := len(subject); idx > 0 {
			for i, ch := range subject {
				if ch == '\n' {
					subject = subject[:i]
					break
				}
			}
		}
		if subject != "" {
			msgs = append(msgs, subject)
		}
		return nil
	})
	// The "stop" sentinel is expected; ignore it.
	if err != nil && err.Error() != "stop" {
		return msgs, nil // best-effort: return what we have
	}
	return msgs, nil
}

// FormatSyncCommitMessage builds a single commit message from a list of
// Forgejo commit subjects. First message becomes the subject line; the rest
// are listed as bullet points in the body.
func FormatSyncCommitMessage(msgs []string) string {
	if len(msgs) == 0 {
		return "chore(sync): incremental sync from Forgejo"
	}
	if len(msgs) == 1 {
		return msgs[0]
	}
	// Most recent commit is the subject; older ones are body bullets.
	var body string
	for i := 1; i < len(msgs); i++ {
		body += "\n- " + msgs[i]
	}
	return msgs[0] + "\n" + body
}

// ChangedFiles returns per-file diffs between lastSHA and the current HEAD
// in the Forgejo worktree at dir.
// If lastSHA is empty, all files in HEAD are returned as additions.
func ChangedFiles(dir, lastSHA string) ([]types.FileDiff, string, error) {
	repo, err := gogit.PlainOpen(dir)
	if err != nil {
		return nil, "", fmt.Errorf("open git repo %s: %w", dir, err)
	}

	head, err := repo.Head()
	if err != nil {
		return nil, "", fmt.Errorf("get HEAD: %w", err)
	}
	currentSHA := head.Hash().String()

	if lastSHA == currentSHA {
		return nil, currentSHA, nil
	}

	headCommit, err := repo.CommitObject(head.Hash())
	if err != nil {
		return nil, "", err
	}
	headTree, err := headCommit.Tree()
	if err != nil {
		return nil, "", err
	}

	var baseTree *object.Tree
	if lastSHA != "" {
		baseHash := plumbing.NewHash(lastSHA)
		if baseCommit, err := repo.CommitObject(baseHash); err == nil {
			baseTree, _ = baseCommit.Tree()
		}
	}

	var filenames []string
	if baseTree == nil {
		// First run: collect all files from HEAD tree.
		err = headTree.Files().ForEach(func(f *object.File) error {
			filenames = append(filenames, f.Name)
			return nil
		})
		if err != nil {
			return nil, currentSHA, err
		}
	} else {
		changes, err := headTree.Diff(baseTree)
		if err != nil {
			return nil, currentSHA, err
		}
		for _, c := range changes {
			name := c.To.Name
			if name == "" {
				name = c.From.Name
			}
			filenames = append(filenames, name)
		}
	}

	// Build FileDiff objects (content rather than patch for sync mode).
	var diffs []types.FileDiff
	for _, name := range filenames {
		f, err := headTree.File(name)
		if err != nil {
			continue
		}
		content, err := f.Contents()
		if err != nil {
			continue
		}
		diffs = append(diffs, types.FileDiff{
			Filename:  name,
			Diff:      content,
			EstTokens: len(content) / 4,
		})
	}

	return diffs, currentSHA, nil
}
