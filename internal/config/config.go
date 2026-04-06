// Package config loads, parses, and validates sentinel configuration.
package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config is the top-level sentinel configuration.
type Config struct {
	Sentinel   SentinelConfig   `yaml:"sentinel"`
	Forgejo    ForgejoConfig    `yaml:"forgejo"`
	GitHub     GitHubConfig     `yaml:"github"`
	Discord    DiscordConfig    `yaml:"discord"`
	PR         PRConfig         `yaml:"pr"`
	Nightly    NightlyConfig    `yaml:"nightly"`
	Digest     DigestConfig     `yaml:"digest"`
	Webhook    WebhookConfig    `yaml:"webhook"`
	Ollama     OllamaConfig     `yaml:"ollama"`
	ClaudeAPI  ClaudeAPIConfig  `yaml:"claude_api"`
	ClaudeCode ClaudeCodeConfig `yaml:"claude_code"`
	Worktree   WorktreeConfig   `yaml:"worktree"`
	Sanitize   SanitizeConfig   `yaml:"sanitize"`
	Allowlist  AllowlistConfig  `yaml:"allowlist"`
	DocGen     DocGenConfig     `yaml:"doc_gen"`
	Obsidian   ObsidianConfig   `yaml:"obsidian"`
	Repos         []RepoConfig  `yaml:"repos"`
	ExcludedRepos []string      `yaml:"excluded_repos"`
}

type SentinelConfig struct {
	GitName         string `yaml:"git_name"`
	GitEmail        string `yaml:"git_email"`
	ForgejoUsername string `yaml:"forgejo_username"`
	GitHubUsername  string `yaml:"github_username"`
}

type ForgejoConfig struct {
	BaseURL string `yaml:"base_url"`
	// Tokens resolved from env: FORGEJO_SENTINEL_TOKEN, FORGEJO_OPERATOR_TOKEN
	SentinelToken string `yaml:"-"`
	OperatorToken string `yaml:"-"`
}

type GitHubConfig struct {
	BaseURL string `yaml:"base_url"`
	Org     string `yaml:"org"`
	// Token resolved from env: GITHUB_TOKEN
	Token string `yaml:"-"`
}

type DiscordConfig struct {
	GuildID          string   `yaml:"guild_id"`
	PRChannelID      string   `yaml:"pr_channel_id"`
	FindingsChannelID string  `yaml:"findings_channel_id"`
	CommandChannelID string   `yaml:"command_channel_id"`
	OperatorUserIDs  []string `yaml:"operator_user_ids"`
	// Token resolved from env: DISCORD_BOT_TOKEN
	BotToken string `yaml:"-"`
}

type PRConfig struct {
	MergeStrategy     string   `yaml:"merge_strategy"`
	HighPriorityTypes []string `yaml:"high_priority_types"`
	MentionOnSecurity bool     `yaml:"mention_on_security"`
	MentionCooldownMinutes int  `yaml:"mention_cooldown_minutes"`
	Housekeeping      HousekeepingConfig `yaml:"housekeeping"`
}

type HousekeepingConfig struct {
	Enabled         bool `yaml:"enabled"`
	OpenOnlyIfContent bool `yaml:"open_only_if_content"`
}

type NightlyConfig struct {
	Cron                      string `yaml:"cron"`
	SkipIfActiveDevWithinHours int   `yaml:"skip_if_active_dev_within_hours"`
	FloodThreshold            int    `yaml:"flood_threshold"`
}

type DigestConfig struct {
	Enabled                   bool `yaml:"enabled"`
	LowPriorityCollapseThreshold int `yaml:"low_priority_collapse_threshold"`
}

type WebhookConfig struct {
	Port              int    `yaml:"port"`
	EventQueueSize    int    `yaml:"event_queue_size"`
	ProcessingWorkers int    `yaml:"processing_workers"`
	ReviewCooldownMinutes int `yaml:"review_cooldown_minutes"`
	// Secret resolved from env: FORGEJO_WEBHOOK_SECRET
	Secret string `yaml:"-"`
}

type OllamaConfig struct {
	Host                string  `yaml:"host"`
	Model               string  `yaml:"model"`
	Temperature         float64 `yaml:"temperature"`
	ContextWindow       int     `yaml:"context_window"`
	ResponseBufferTokens int    `yaml:"response_buffer_tokens"`
}

type ClaudeAPIConfig struct {
	Model             string `yaml:"model"`
	MaxTokens         int    `yaml:"max_tokens"`
	RPMLimit          int    `yaml:"rpm_limit"`
	RateLimitBufferMs int    `yaml:"rate_limit_buffer_ms"`
	// API key resolved from env: ANTHROPIC_API_KEY
	APIKey string `yaml:"-"`
}

type ClaudeCodeConfig struct {
	BinaryPath         string   `yaml:"binary_path"`
	Flags              []string `yaml:"flags"`
	TaskTimeoutMinutes int      `yaml:"task_timeout_minutes"`
}

type WorktreeConfig struct {
	BasePath string `yaml:"base_path"`
}

type SanitizeConfig struct {
	HighConfidenceThreshold   float64           `yaml:"high_confidence_threshold"`
	MediumConfidenceThreshold float64           `yaml:"medium_confidence_threshold"`
	SkipPatterns              []string          `yaml:"skip_patterns"`
	CategoryReasons           map[string]string `yaml:"category_reasons"`
	ScrubPatterns             []ScrubPattern    `yaml:"scrub_patterns"`
	// Layer2TimeoutSeconds bounds a single Ollama Layer-2 call. On timeout or
	// any Ollama error, Layer 2 falls back to the [AI_ASSISTANT] API (if configured).
	// Zero or negative uses the default of 60 seconds.
	Layer2TimeoutSeconds int `yaml:"layer2_timeout_seconds"`
}

// ScrubPattern defines a regex substitution applied to all file content
// before it is pushed to the GitHub mirror.
type ScrubPattern struct {
	Pattern     string `yaml:"pattern"`     // RE2 regular expression
	Replacement string `yaml:"replacement"` // replacement string (may use $1 etc.)
}

type AllowlistConfig struct {
	ConfirmationTTLMinutes int `yaml:"confirmation_ttl_minutes"`
}

// DocGenConfig controls documentation generation behaviour.
type DocGenConfig struct {
	Enabled         bool     `yaml:"enabled"`
	DefaultTargets  []string `yaml:"default_targets"`  // doc files to maintain across all repos
	MaxContextFiles int      `yaml:"max_context_files"` // max source files to list in prompt
}

// ObsidianConfig points to the local Obsidian vault git repo.
// The vault is written to directly (no PR); caller manages git push.
type ObsidianConfig struct {
	// VaultPath is the absolute path to the Obsidian vault directory.
	// Recommended: rename the repo to andusystems-obsidian to follow workspace naming.
	VaultPath    string `yaml:"vault_path"`
	ChangelogDir string `yaml:"changelog_dir"` // subdir for per-repo changelogs
	DocsDir      string `yaml:"docs_dir"`      // subdir for per-repo doc snapshots
}

type RepoConfig struct {
	Name           string   `yaml:"name"`
	ForgejoPath    string   `yaml:"forgejo_path"`
	GitHubPath     string   `yaml:"github_path"`
	Description    string   `yaml:"description"`
	Languages      []string `yaml:"languages"`
	FocusAreas     []string `yaml:"focus_areas"`
	MaxTasksPerRun int      `yaml:"max_tasks_per_run"`
	MergeStrategy  string   `yaml:"merge_strategy"`
	SyncEnabled    bool     `yaml:"sync_enabled"`
	Excluded       bool     `yaml:"excluded"`
	// DocTargets overrides doc_gen.default_targets for this repo.
	// Leave empty to use the global defaults.
	DocTargets []string `yaml:"doc_targets"`
}

// Load reads and parses the config file at path, then resolves env vars.
// Must-NOT make network calls or access filesystem beyond the config file.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %q: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %q: %w", path, err)
	}

	if err := resolveEnv(&cfg); err != nil {
		return nil, fmt.Errorf("resolve env: %w", err)
	}

	if err := validate(&cfg); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}

	applyDefaults(&cfg)

	return &cfg, nil
}

// applyDefaults fills in zero-value fields with sensible defaults.
func applyDefaults(cfg *Config) {
	if cfg.PR.MergeStrategy == "" {
		cfg.PR.MergeStrategy = "squash"
	}
	if len(cfg.PR.HighPriorityTypes) == 0 {
		cfg.PR.HighPriorityTypes = []string{"code", "fix", "feat", "vulnerability"}
	}
	if cfg.PR.MentionCooldownMinutes == 0 {
		cfg.PR.MentionCooldownMinutes = 60
	}
	if cfg.Nightly.Cron == "" {
		cfg.Nightly.Cron = "0 23 * * *"
	}
	if cfg.Nightly.SkipIfActiveDevWithinHours == 0 {
		cfg.Nightly.SkipIfActiveDevWithinHours = 2
	}
	// -1 means "disabled" — leave as-is so preflight can detect it.
	if cfg.Nightly.FloodThreshold == 0 {
		cfg.Nightly.FloodThreshold = 5
	}
	if cfg.Webhook.Port == 0 {
		cfg.Webhook.Port = 8080
	}
	if cfg.Webhook.EventQueueSize == 0 {
		cfg.Webhook.EventQueueSize = 100
	}
	if cfg.Webhook.ProcessingWorkers == 0 {
		cfg.Webhook.ProcessingWorkers = 4
	}
	if cfg.Webhook.ReviewCooldownMinutes == 0 {
		cfg.Webhook.ReviewCooldownMinutes = 5
	}
	if cfg.Ollama.ContextWindow == 0 {
		cfg.Ollama.ContextWindow = 16384
	}
	if cfg.Ollama.ResponseBufferTokens == 0 {
		cfg.Ollama.ResponseBufferTokens = 2048
	}
	if cfg.ClaudeAPI.MaxTokens == 0 {
		cfg.ClaudeAPI.MaxTokens = 8192
	}
	if cfg.ClaudeAPI.RPMLimit == 0 {
		cfg.ClaudeAPI.RPMLimit = 50
	}
	if cfg.ClaudeCode.BinaryPath == "" {
		cfg.ClaudeCode.BinaryPath = "/usr/local/bin/[AI_ASSISTANT]"
	}
	if cfg.ClaudeCode.TaskTimeoutMinutes == 0 {
		cfg.ClaudeCode.TaskTimeoutMinutes = 30
	}
	if cfg.Worktree.BasePath == "" {
		cfg.Worktree.BasePath = "/data/workspace"
	}
	if cfg.Sanitize.HighConfidenceThreshold == 0 {
		cfg.Sanitize.HighConfidenceThreshold = 0.9
	}
	if cfg.Sanitize.MediumConfidenceThreshold == 0 {
		cfg.Sanitize.MediumConfidenceThreshold = 0.6
	}
	if cfg.Allowlist.ConfirmationTTLMinutes == 0 {
		cfg.Allowlist.ConfirmationTTLMinutes = 10
	}
	if cfg.Digest.LowPriorityCollapseThreshold == 0 {
		cfg.Digest.LowPriorityCollapseThreshold = 5
	}
	if cfg.Sentinel.GitName == "" {
		cfg.Sentinel.GitName = "Sentinel"
	}
	if cfg.Sentinel.GitEmail == "" {
		cfg.Sentinel.GitEmail = "sentinel@andusystems.com"
	}
	if len(cfg.DocGen.DefaultTargets) == 0 {
		cfg.DocGen.DefaultTargets = []string{
			"README.md",
			"docs/architecture.md",
			"docs/development.md",
			"CHANGELOG.md",
		}
	}
	if cfg.DocGen.MaxContextFiles == 0 {
		cfg.DocGen.MaxContextFiles = 60
	}
	if cfg.Obsidian.ChangelogDir == "" {
		cfg.Obsidian.ChangelogDir = "changelogs"
	}
	if cfg.Obsidian.DocsDir == "" {
		cfg.Obsidian.DocsDir = "repos"
	}
}
