// Package [AI_ASSISTANT] implements types.ClaudeAPIClient by shelling out to the
// [AI_ASSISTANT] Code CLI. Sentinel uses this for:
//   - Layer 3 sanitization safety-net pass
//   - Layer 2 fallback when Ollama times out or fails
//
// Only the [AI_ASSISTANT] Code CLI is invoked here — no direct [AI_PROVIDER] API calls.
// All invocations share the global [AI_ASSISTANT] Code semaphore (executor package),
// so CLI subprocesses never run concurrently.
package [AI_ASSISTANT]

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"

	"github.com/andusystems/sentinel/internal/executor"
	"github.com/andusystems/sentinel/internal/llm"
	"github.com/andusystems/sentinel/internal/types"
)

// Client implements types.ClaudeAPIClient by invoking the [AI_ASSISTANT] Code CLI.
type Client struct {
	binary    string
	baseFlags []string
	timeout   time.Duration
}

// NewClient builds a client that runs the [AI_ASSISTANT] Code binary at binaryPath
// with the given base flags (e.g. --output-format=json). requestTimeout
// bounds a single CLI invocation.
func NewClient(binaryPath string, baseFlags []string, requestTimeout time.Duration) *Client {
	if requestTimeout <= 0 {
		requestTimeout = 2 * time.Minute
	}
	// Defensive copy so the caller's slice isn't mutated.
	flags := append([]string(nil), baseFlags...)
	return &Client{binary: binaryPath, baseFlags: flags, timeout: requestTimeout}
}

// SanitizeChunk runs [AI_ASSISTANT] Code with the Role D system prompt and the file
// content as the user prompt. Output is the JSON array of findings.
func (c *Client) SanitizeChunk(ctx context.Context, content string) ([]types.SanitizationFinding, error) {
	executor.AcquireClaudeCode()
	defer executor.ReleaseClaudeCode()

	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	// Build the prompt: Role D system prompt + user content. [AI_ASSISTANT] Code in
	// --print mode expects a single combined prompt; there's no --system
	// flag in this version of the CLI.
	var prompt strings.Builder
	prompt.WriteString(llm.RoleDSystem)
	prompt.WriteString("\n\n---\n\n")
	prompt.WriteString("Identify any remaining sensitive values not already redacted. Return ONLY a JSON array. No prose before or after.\n\n")
	prompt.WriteString("Content:\n")
	prompt.WriteString(content)

	args := append([]string(nil), c.baseFlags...)
	args = append(args, "--print")

	cmd := exec.CommandContext(callCtx, c.binary, args...)
	cmd.Stdin = strings.NewReader(prompt.String())
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("[AI_ASSISTANT] code cli: %v (stderr: %s)", err, truncate(stderr.String(), 500))
	}

	resultText, err := extractResultText(stdout.Bytes())
	if err != nil {
		return nil, fmt.Errorf("parse [AI_ASSISTANT] code output: %w", err)
	}
	if resultText == "" {
		return nil, nil
	}

	findings, err := extractFindings(resultText)
	if err != nil {
		slog.Warn("[AI_ASSISTANT] code: malformed JSON array", "err", err)
		return nil, nil
	}
	return findings, nil
}

// extractResultText reads the [AI_ASSISTANT] Code --output-format=json envelope and
// returns the .result field. Falls back to the raw stdout if the envelope
// isn't a structured JSON object (e.g. non-JSON output-format).
func extractResultText(stdout []byte) (string, error) {
	trimmed := bytes.TrimSpace(stdout)
	if len(trimmed) == 0 {
		return "", nil
	}
	// Expect a JSON object envelope from --output-format=json.
	if trimmed[0] == '{' {
		var env struct {
			Type    string `json:"type"`
			Subtype string `json:"subtype"`
			IsError bool   `json:"is_error"`
			Result  string `json:"result"`
		}
		if err := json.Unmarshal(trimmed, &env); err == nil {
			if env.IsError {
				return "", fmt.Errorf("[AI_ASSISTANT] code reported error: %s", truncate(env.Result, 500))
			}
			return env.Result, nil
		}
	}
	// Not a JSON envelope — treat stdout as the raw result.
	return string(trimmed), nil
}

// extractFindings pulls a JSON array of SanitizationFinding from an LLM
// response that may contain leading/trailing prose.
func extractFindings(response string) ([]types.SanitizationFinding, error) {
	response = strings.TrimSpace(response)
	if response == "" {
		return nil, nil
	}

	var findings []types.SanitizationFinding
	if err := json.Unmarshal([]byte(response), &findings); err == nil {
		return findings, nil
	}

	start := strings.IndexByte(response, '[')
	end := strings.LastIndexByte(response, ']')
	if start >= 0 && end > start {
		jsonPart := response[start : end+1]
		if err := json.Unmarshal([]byte(jsonPart), &findings); err == nil {
			return findings, nil
		}
	}
	return nil, fmt.Errorf("no JSON array in response")
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
