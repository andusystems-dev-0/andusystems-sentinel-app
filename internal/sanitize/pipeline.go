package sanitize

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"sort"
	"time"

	"github.com/andusystems/sentinel/internal/config"
	"github.com/andusystems/sentinel/internal/types"
)

// Pipeline implements types.SanitizationPipeline.
// Layer sequencing:
//  1. Layer 1 — gitleaks (high confidence, auto-redact)
//  2. Layer 2 — Local LLM (medium/low confidence, pending resolution)
//  3. Layer 3 — [AI_ASSISTANT] API (final safety net; only if APIKey configured)
//
// All layers respect SkipZones (approved_values byte ranges).
type Pipeline struct {
	l1   *layer1Gitleaks
	l2   *layer2LLM
	l3   *layer3Claude
	cfg  *config.SanitizeConfig
}

// NewPipeline constructs a SanitizationPipeline.
// [AI_ASSISTANT] may be nil — Layer 3 is skipped AND Layer 2 has no [AI_ASSISTANT] fallback
// if the API key is not configured.
func NewPipeline(llm types.LLMClient, [AI_ASSISTANT] types.ClaudeAPIClient, cfg *config.SanitizeConfig) (*Pipeline, error) {
	l1, err := newLayer1Gitleaks()
	if err != nil {
		return nil, err
	}

	var l3 *layer3Claude
	if [AI_ASSISTANT] != nil {
		l3 = newLayer3Claude([AI_ASSISTANT])
	}

	timeout := time.Duration(cfg.Layer2TimeoutSeconds) * time.Second
	return &Pipeline{
		l1:  l1,
		l2:  newLayer2LLM(llm, [AI_ASSISTANT], timeout),
		l3:  l3,
		cfg: cfg,
	}, nil
}

// SanitizeFile runs all three layers on a file and returns staged content + findings.
func (p *Pipeline) SanitizeFile(ctx context.Context, opts types.SanitizeFileOpts) (*types.SanitizeFileResult, error) {
	return p.run(ctx, opts)
}

// ReanalyzeFile re-runs all three layers (used when operator requests 🔍 re-analysis).
func (p *Pipeline) ReanalyzeFile(ctx context.Context, opts types.SanitizeFileOpts) (*types.SanitizeFileResult, error) {
	return p.run(ctx, opts)
}

func (p *Pipeline) run(ctx context.Context, opts types.SanitizeFileOpts) (*types.SanitizeFileResult, error) {
	// Auto-detect template expressions (Jinja2, HCL, ERB, etc.) and protect
	// them from all layers — regardless of what words they contain.
	templateZones := templateSkipZones(opts.Content)
	zones := mergeSkipZones(append(opts.SkipZones, templateZones...))

	// --- Layer 1: gitleaks ---
	l1Findings, err := p.l1.scan(ctx, opts.Repo, opts.Filename, opts.Content, zones, opts.SyncRunID, p.cfg)
	if err != nil {
		return nil, err
	}
	assignIDs(l1Findings)

	// Build intermediate content: replace Layer 1 high-confidence findings.
	// This is what Layers 2 and 3 see.
	// We do a staging pass here to get the post-L1 content, but we don't
	// use the token_index yet — full staging is done in BuildStagingContent
	// after all layers complete.
	postL1Content, _ := buildIntermediateContent(opts.Content, l1Findings, p.cfg)

	// --- Layer 2: Local LLM ---
	l2Findings, err := p.l2.scan(ctx, opts.Repo, opts.Filename, postL1Content, zones, opts.SyncRunID)
	if err != nil {
		return nil, err
	}
	// Remap byte offsets from postL1Content back to original (approximate).
	// For simplicity, we keep offsets relative to the layered content;
	// the staging builder operates on original content with all findings.
	assignIDs(l2Findings)

	// --- Layer 3: [AI_ASSISTANT] API ---
	postL2Content, _ := buildIntermediateContent(postL1Content, l2Findings, p.cfg)
	var l3Findings []types.SanitizationFinding
	if p.l3 != nil {
		l3Findings, err = p.l3.scan(ctx, opts.Repo, opts.Filename, postL2Content, zones, opts.SyncRunID)
		if err != nil {
			return nil, err
		}
		assignIDs(l3Findings)
	}

	// Merge all findings; sort by original byte offset.
	all := append(append(l1Findings, l2Findings...), l3Findings...)
	sort.Slice(all, func(i, j int) bool {
		return all[i].ByteOffsetStart < all[j].ByteOffsetStart
	})

	// Final staging pass: assign token_index and build staged content.
	staged, all := BuildStagingContent(opts.Content, all, p.cfg)

	// Apply scrub patterns (e.g. remove AI assistant references).
	if len(p.cfg.ScrubPatterns) > 0 {
		scrubbed, err := ApplyScrubPatterns(staged, p.cfg.ScrubPatterns)
		if err != nil {
			slog.Warn("scrub pattern error", "file", opts.Filename, "err", err)
		} else {
			staged = scrubbed
		}
	}

	return &types.SanitizeFileResult{
		SanitizedContent: staged,
		Findings:         all,
	}, nil
}

// buildIntermediateContent builds an intermediate version of content where
// Layer N high-confidence findings are replaced with tags, so Layer N+1 does not
// re-flag already-identified values.
func buildIntermediateContent(content []byte, findings []types.SanitizationFinding, cfg *config.SanitizeConfig) ([]byte, []types.SanitizationFinding) {
	// Only replace high-confidence findings in intermediate passes.
	var high []types.SanitizationFinding
	for _, f := range findings {
		if f.Confidence == "high" {
			high = append(high, f)
		}
	}
	if len(high) == 0 {
		return content, findings
	}
	out, _ := BuildStagingContent(content, high, cfg)
	return out, findings
}

// assignIDs generates random IDs for findings that don't have one.
func assignIDs(findings []types.SanitizationFinding) {
	for i := range findings {
		if findings[i].ID == "" {
			findings[i].ID = newFindingID()
		}
	}
}

func newFindingID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}
