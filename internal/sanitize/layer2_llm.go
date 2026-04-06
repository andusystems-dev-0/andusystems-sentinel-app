package sanitize

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/andusystems/sentinel/internal/types"
)

// layer2LLM calls LLMClient.SanitizeChunk (Role D — Sanitization Semantic Pass).
// It runs on the post-Layer-1 content (with high-confidence findings already replaced).
// If Ollama exceeds timeout or errors, and a [AI_ASSISTANT] fallback is configured, the
// same content is sent to [AI_ASSISTANT].
type layer2LLM struct {
	llm      types.LLMClient
	fallback types.ClaudeAPIClient
	timeout  time.Duration
}

func newLayer2LLM(llm types.LLMClient, fallback types.ClaudeAPIClient, timeout time.Duration) *layer2LLM {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	return &layer2LLM{llm: llm, fallback: fallback, timeout: timeout}
}

// scan calls the LLM with the post-Layer-1 content and returns any additional findings.
// Findings already covered by Layer 1 (auto-redacted) are excluded from the content
// by the staging construction — Layer 2 only sees remaining content.
func (l *layer2LLM) scan(
	ctx context.Context,
	repo, filename string,
	content []byte,
	zones []types.SkipZone,
	syncRunID string,
) ([]types.SanitizationFinding, error) {
	callCtx, cancel := context.WithTimeout(ctx, l.timeout)
	defer cancel()

	raw, err := l.llm.SanitizeChunk(callCtx, string(content))
	if err != nil {
		timedOut := errors.Is(callCtx.Err(), context.DeadlineExceeded)
		if l.fallback != nil {
			slog.Warn("layer2 Ollama failed, falling back to [AI_ASSISTANT]",
				"repo", repo, "file", filename, "timeout", timedOut, "err", err)
			raw, err = l.fallback.SanitizeChunk(ctx, string(content))
			if err != nil {
				slog.Warn("layer2 [AI_ASSISTANT] fallback error",
					"repo", repo, "file", filename, "err", err)
				return nil, nil
			}
		} else {
			slog.Warn("layer2 LLM sanitize error",
				"repo", repo, "file", filename, "timeout", timedOut, "err", err)
			return nil, nil // non-fatal: proceed to Layer 3
		}
	}

	var valid []types.SanitizationFinding
	for _, f := range raw {
		if f.OriginalValue == "" {
			slog.Warn("layer2 finding has no original_value (discarded)", "file", filename)
			continue
		}

		// Recompute byte offsets from original_value — LLM offsets are unreliable.
		if !resolveByteOffsets(content, &f) {
			slog.Warn("layer2 finding original_value not found in content (discarded)",
				"file", filename, "value_len", len(f.OriginalValue))
			continue
		}

		if isInSkipZone(f.ByteOffsetStart, f.ByteOffsetEnd, zones) {
			continue
		}
		f.Layer = 2
		f.Repo = repo
		f.Filename = filename
		f.SyncRunID = syncRunID
		valid = append(valid, f)
	}
	return valid, nil
}

// resolveByteOffsets searches content for f.OriginalValue and sets ByteOffsetStart/End.
// Uses f.LineNumber to disambiguate when the value appears multiple times.
// Returns false if the value cannot be found.
func resolveByteOffsets(content []byte, f *types.SanitizationFinding) bool {
	needle := []byte(f.OriginalValue)
	if len(needle) == 0 {
		return false
	}

	// If a line number was given, prefer a match on that line.
	if f.LineNumber > 0 {
		lineStart := findLineStart(content, f.LineNumber)
		if lineStart >= 0 {
			lineEnd := bytes.IndexByte(content[lineStart:], '\n')
			var line []byte
			if lineEnd < 0 {
				line = content[lineStart:]
			} else {
				line = content[lineStart : lineStart+lineEnd]
			}
			if idx := bytes.Index(line, needle); idx >= 0 {
				f.ByteOffsetStart = lineStart + idx
				f.ByteOffsetEnd = f.ByteOffsetStart + len(needle)
				return true
			}
		}
	}

	// Fall back to first occurrence in entire content.
	idx := bytes.Index(content, needle)
	if idx < 0 {
		return false
	}
	f.ByteOffsetStart = idx
	f.ByteOffsetEnd = idx + len(needle)
	return true
}

// findLineStart returns the byte offset of the start of the given 1-based line number.
func findLineStart(content []byte, lineNumber int) int {
	line := 1
	for i, b := range content {
		if line == lineNumber {
			return i
		}
		if b == '\n' {
			line++
		}
	}
	return -1
}
