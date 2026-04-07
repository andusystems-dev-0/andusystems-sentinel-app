# Sentinel

Autonomous SDLC orchestration engine. Sentinel monitors Forgejo repositories, runs nightly code analysis via a local LLM, sanitizes secrets before mirroring to GitHub, and surfaces everything through Discord with operator-gated reactions.

## What It Does

| Mode | Trigger | What Happens |
|------|---------|--------------|
| **Mode 1 — Nightly** | Cron (default 23:00) | Diffs Forgejo HEAD vs last run, LLM analysis, opens fix/feat/docs PRs on Forgejo, Discord notification |
| **Mode 2 — PR Review** | Forgejo webhook (`pull_request`) | Reviews developer PRs via LLM, posts verdict + per-file notes, optionally opens housekeeping companion PR |
| **Mode 3 — Sync** | Push webhook or manual | Sanitizes changed files, pushes clean content to GitHub mirror, Discord alert on findings |
| **Mode 4 — Migration** | Manual (`--mode migrate`) | One-time full-repo scan, sanitizes all files, pushes to GitHub mirror, Discord confirmation flow |

Operator decisions (merge, close, approve finding, reject finding) happen entirely through Discord emoji reactions. Forgejo actions taken in the UI are reflected back to Discord automatically via webhooks.

## Architecture Overview

```
Forgejo ──webhook──► Sentinel ──► Discord
   ▲                    │
   │              ┌─────┼──────┐
   │           Ollama  SQLite  GitHub
   │           (LLM)   (DB)   (mirror)
   └── PRs ◄───────────┘
```

- **Single SQLite database** — all state, audit log, pending findings, PR records
- **Two git worktrees** — one cloned from Forgejo (source of truth) and one for sanitized staging (pushed to GitHub)
- **Three-layer sanitization** — gitleaks (L1, auto-redact) → Ollama (L2, operator review) → [AI_ASSISTANT] API (L3, optional safety net)
- **Per-repo RW locking** — `sync.RWMutex` per repo prevents concurrent worktree corruption
- **Webhook async ACK** — HTTP 200 returned immediately; processing happens in a worker pool
- **Operator-gated reactions** — only Discord user IDs in `operator_user_ids` can trigger Forgejo actions
- **Drift reconciliation** — periodic checks catch missed webhooks and trigger Mode 3 sync automatically

For detailed architecture documentation, see [docs/architecture.md](docs/architecture.md).

## Quick Start

### Prerequisites

| Component | Requirement |
|-----------|-------------|
| **Go** | 1.24+ (no CGo required) |
| **Forgejo** | Running instance with two tokens: `sentinel` service account (read/write PRs, no merge) and an operator token (merge only) |
| **GitHub** | Organisation or account for mirror repos; PAT with `repo` scope |
| **Discord** | Bot application with message content and reaction intents; three channels (PRs, findings, commands) |
| **Ollama** | Running with `qwen2.5-coder:14b` pulled; required for nightly analysis and sanitization Layer 2 |
| **Storage** | Local directory (dev) or RWO PVC (Kubernetes); SQLite requires exclusive file access |

**Optional:**

| Component | Used For |
|-----------|----------|
| **[AI_PROVIDER] API key** | Sanitization Layer 3 (additional semantic safety-net pass via [AI_ASSISTANT] API) |
| **[AI_ASSISTANT] Code CLI** | Mode 1 task execution for `fix`, `feat`, `vulnerability`, `refactor` task types |

### 1. Build

```bash
make build
# Binary at ./bin/sentinel
```

### 2. Configure

```bash
cp config.yaml.example config.yaml
# Edit config.yaml with your Forgejo URL, GitHub org, Discord channel IDs, etc.
```

### 3. Set Environment Variables

All secrets come from environment variables, never from the config file.

```bash
export FORGEJO_SENTINEL_TOKEN="<sentinel-account-token>"
export FORGEJO_OPERATOR_TOKEN="<operator-token-with-merge-perms>"
export DISCORD_BOT_TOKEN="Bot <your-bot-token>"
export GITHUB_TOKEN="<github-pat>"
export FORGEJO_WEBHOOK_SECRET="$(openssl rand -hex 32)"
# Optional:
export ANTHROPIC_API_KEY=""
```

For local development, put these in a `.env` file (never committed). Sentinel loads it automatically.

### 4. Initial Migration (per repo)

Run once per repo before starting the daemon:

```bash
./bin/sentinel --config config.yaml --mode migrate --repo myrepo
```

Use `--force` if the GitHub mirror already has content. Sentinel posts a confirmation message in Discord — react to approve or cancel.

### 5. Start the Daemon

```bash
./bin/sentinel --config config.yaml
```

This starts the webhook HTTP server, Discord bot, drift reconciler, and nightly cron scheduler.

## Run Modes

```bash
# Full daemon (webhook server + Discord bot + cron)
./bin/sentinel --config config.yaml

# Nightly pipeline for one repo
./bin/sentinel --config config.yaml --mode nightly --repo myrepo

# Nightly pipeline for all repos
./bin/sentinel --config config.yaml --mode nightly

# Incremental sync (dry-run: no GitHub push)
./bin/sentinel --config config.yaml --mode sync --repo myrepo --dry-run

# Full migration
./bin/sentinel --config config.yaml --mode migrate --repo myrepo --force

# Documentation generation
./bin/sentinel --config config.yaml --mode doc-gen --repo myrepo
```

## Configuration Reference

### Environment Variables

| Variable | Required | Description |
|----------|----------|-------------|
| `FORGEJO_SENTINEL_TOKEN` | Yes | Read-only Forgejo service account token |
| `FORGEJO_OPERATOR_TOKEN` | Yes | Merge-only token; used in exactly one code path |
| `DISCORD_BOT_TOKEN` | Yes | Full bot token including `Bot ` prefix |
| `GITHUB_TOKEN` | Yes | PAT with `repo` scope |
| `FORGEJO_WEBHOOK_SECRET` | Yes | HMAC-SHA256 shared secret for webhook validation |
| `ANTHROPIC_API_KEY` | No | Enables sanitization Layer 3 ([AI_ASSISTANT] API) |
| `SENTINEL_DB_PATH` | No | SQLite database path (default: `/data/db/sentinel.db`) |
| `SENTINEL_INGRESS_HOST` | No | If set, auto-registers Forgejo webhooks at startup |

### Config File Sections

The full configuration is in `config.yaml`. Key sections:

| Section | Purpose |
|---------|---------|
| `sentinel` | Git identity for commits (name, email, usernames) |
| `forgejo` | Forgejo instance base URL |
| `github` | GitHub API base URL and organisation |
| `discord` | Guild ID, channel IDs, operator user IDs |
| `pr` | Merge strategy, priority types, mention settings, housekeeping |
| `nightly` | Cron schedule, active-dev skip window, flood threshold |
| `webhook` | Listener port, queue size, worker count, review cooldown |
| `ollama` | Host, model, temperature, context window |
| `claude_api` | Model, max tokens, rate limits |
| `claude_code` | Binary path, CLI flags, task timeout |
| `worktree` | Base path for git worktrees |
| `digest` | Nightly digest display settings (enabled, collapse threshold) |
| `sanitize` | Confidence thresholds, skip patterns, category reasons |
| `allowlist` | Approved-value allowlist settings (confirmation TTL) |
| `repos` | Per-repo configuration (paths, languages, focus areas, sync settings) |
| `excluded_repos` | List of repo names to skip entirely |

See `config.yaml.example` for the complete annotated reference.

### Detailed Configuration Options

#### `sentinel`

| Key | Default | Description |
|-----|---------|-------------|
| `git_name` | — | Git author name for sentinel commits |
| `git_email` | — | Git author email for sentinel commits |
| `forgejo_username` | — | Forgejo service account username |
| `github_username` | — | GitHub bot username for mirror repos |

#### `forgejo`

| Key | Default | Description |
|-----|---------|-------------|
| `base_url` | — | Base URL of the Forgejo instance (e.g. `https://git.andusystems.com`) |

Tokens are set via environment variables (`FORGEJO_SENTINEL_TOKEN`, `FORGEJO_OPERATOR_TOKEN`).

#### `github`

| Key | Default | Description |
|-----|---------|-------------|
| `base_url` | `https://api.github.com` | GitHub API base URL |
| `org` | — | GitHub organisation for mirror repos |

Token is set via `GITHUB_TOKEN` environment variable.

#### `discord`

| Key | Default | Description |
|-----|---------|-------------|
| `guild_id` | — | Discord server (guild) ID |
| `actions_channel_id` | — | Channel for interactive embeds: PR actions, migration confirmations, `/sentinel` commands |
| `logs_channel_id` | — | Channel for informational embeds: findings, sync/migration status, errors |
| `operator_user_ids` | `[]` | Allowlist of Discord user IDs that can approve, merge, or reject |

#### `pr`

| Key | Default | Description |
|-----|---------|-------------|
| `merge_strategy` | `squash` | Default merge strategy: `squash`, `merge`, or `rebase` |
| `high_priority_types` | `["code","fix","feat","vulnerability"]` | Task types treated as high priority |
| `mention_on_security` | `true` | Whether to `@here` in Discord on security-related findings |
| `mention_cooldown_minutes` | `60` | Minimum minutes between `@here` mentions per repo |
| `housekeeping.enabled` | `true` | Enable housekeeping companion PRs |
| `housekeeping.open_only_if_content` | `true` | Only open housekeeping PR if files actually changed |

#### `nightly`

| Key | Default | Description |
|-----|---------|-------------|
| `cron` | `0 23 * * *` | Cron expression for nightly pipeline schedule |
| `skip_if_active_dev_within_hours` | `2` | Skip nightly run if non-sentinel commits occurred in the last N hours |
| `flood_threshold` | `5` | Maximum open sentinel PRs per repo before skipping new runs |

#### `digest`

| Key | Default | Description |
|-----|---------|-------------|
| `enabled` | `true` | Enable nightly digest summaries posted to Discord |
| `low_priority_collapse_threshold` | `5` | If more than N low-priority open PRs exist, show a count instead of a full list |

#### `webhook`

| Key | Default | Description |
|-----|---------|-------------|
| `port` | `8080` | HTTP listener port for incoming Forgejo webhooks |
| `event_queue_size` | `100` | Buffered channel size for the event queue (returns 429 when full) |
| `processing_workers` | `4` | Number of concurrent webhook processing workers |
| `review_cooldown_minutes` | `5` | Deduplication window for PR review triggers |

#### `ollama`

| Key | Default | Description |
|-----|---------|-------------|
| `host` | — | Ollama API endpoint URL |
| `model` | `qwen2.5-coder:14b` | LLM model for analysis and sanitization |
| `temperature` | `0.1` | Sampling temperature |
| `context_window` | `16384` | Model context window size in tokens |
| `response_buffer_tokens` | `2048` | Tokens reserved for LLM response (subtracted from context window for input) |

#### `claude_api`

| Key | Default | Description |
|-----|---------|-------------|
| `model` | `[AI_ASSISTANT]-sonnet-4-6` | [AI_ASSISTANT] model ID for Layer 3 sanitization |
| `max_tokens` | `8192` | Maximum response tokens per API call |
| `rpm_limit` | `50` | Requests per minute rate limit |
| `rate_limit_buffer_ms` | `200` | Minimum milliseconds between API calls |

API key is set via `ANTHROPIC_API_KEY` environment variable.

#### `claude_code`

| Key | Default | Description |
|-----|---------|-------------|
| `binary_path` | `/usr/local/bin/[AI_ASSISTANT]` | Path to the [AI_ASSISTANT] Code CLI binary |
| `flags` | `["--output-format=json","--dangerously-skip-permissions"]` | CLI flags passed to every invocation |
| `task_timeout_minutes` | `30` | Maximum minutes per [AI_ASSISTANT] Code task before timeout |

#### `worktree`

| Key | Default | Description |
|-----|---------|-------------|
| `base_path` | `/data/workspace` | Base directory for git worktrees (PVC mount point in Kubernetes) |

#### `sanitize`

| Key | Default | Description |
|-----|---------|-------------|
| `high_confidence_threshold` | `0.9` | Findings at or above this confidence are auto-redacted (Layer 1) |
| `medium_confidence_threshold` | `0.6` | Findings at or above this confidence go to operator review |
| `skip_patterns` | `["*.test","testdata/**","fixtures/**","*.example"]` | Glob patterns for files excluded from sanitization |
| `category_reasons` | *(see below)* | Map of category → reason string used in `SENTINEL BOT` redaction tags |

Built-in categories: `SECRET`, `API_KEY`, `PASSWORD`, `PRIVATE_KEY`, `CONNECTION_STRING`, `INTERNAL_URL`. Reason strings must not contain `>` (validated at startup).

#### `allowlist`

| Key | Default | Description |
|-----|---------|-------------|
| `confirmation_ttl_minutes` | `10` | TTL for allowlist confirmation prompts before they expire |

#### `repos`

Each entry in the `repos` list configures a monitored repository:

| Key | Default | Description |
|-----|---------|-------------|
| `name` | — | Short name used in CLI flags (`--repo`) and Discord labels |
| `forgejo_path` | — | Forgejo `owner/repo` path |
| `github_path` | — | GitHub `owner/repo` mirror path |
| `languages` | `[]` | Languages in the repo (used by LLM for analysis context) |
| `focus_areas` | `[]` | Areas to prioritise in nightly analysis (e.g. `security`, `performance`) |
| `max_tasks_per_run` | `10` | Maximum tasks created per nightly run |
| `merge_strategy` | *(global `pr.merge_strategy`)* | Per-repo merge strategy override |
| `sync_enabled` | `false` | Whether Mode 3 sync is active for this repo |
| `excluded` | `false` | If `true`, skip this repo entirely |

#### `excluded_repos`

A top-level list of repo names to skip entirely (alternative to setting `excluded: true` per repo):

```yaml
excluded_repos:
  - "legacy-app"
  - "archived-service"
```

## Sanitization Pipeline

Every file flowing from Forgejo to GitHub passes through three layers:

```
Input file
    │
    ▼
[Skip zone pre-scan] — previously approved values excluded
    │
    ▼
Layer 1: gitleaks + regex
  ≥ 0.9 confidence → auto-redact
  < 0.9 confidence → pass to Layer 2
    │
    ▼
Layer 2: Ollama (semantic analysis)
  Any finding → pending operator review
    │
    ▼
Layer 3: [AI_ASSISTANT] API (optional safety net)
  Any new finding → pending operator review
    │
    ▼
Staged output with SENTINEL BOT tags
```

**Discord reactions for findings:**
- ✅ Approve suggested replacement
- ❌ Reject (keep original in Forgejo only)
- ✏️ Provide custom replacement in thread
- 🔍 Re-analyse with LLM

**Discord reactions for PRs:**
- ✅ Merge via Forgejo API
- ❌ Close without merging
- 💬 Open discussion thread

## Kubernetes Deployment

```bash
# Build and push
make docker-build docker-push

# Install with Helm
helm install sentinel charts/sentinel \
  -n sentinel --create-namespace \
  -f charts/sentinel/values.yaml \
  -f values-prod.yaml
```

Key constraints:
- **Replicas must be 1** — SQLite requires exclusive PVC access
- **RWO volumes only** — never use ReadWriteMany
- **ArgoCD sync is manual only** — sentinel has side effects on external systems

See [docs/development.md](docs/development.md) for build and test commands.

## LLM Roles

| Role | Purpose |
|------|---------|
| A — Analyst | Nightly code analysis; identifies improvement tasks |
| B — Reviewer | Reviews developer PRs; produces verdict + per-file notes |
| C — Prose | General prose generation (PR bodies, summaries) |
| D — Sanitize | Semantic secret detection in file content |
| E — Finding thread | Answers questions about findings in Discord threads |
| F — PR thread | Answers questions about PRs in Discord threads |
| G — Housekeeping | Generates companion PR bodies for housekeeping changes |

## Security Model

- **Token isolation** — sentinel token has no merge permission; operator token used in exactly one code path
- **Webhook validation** — HMAC-SHA256 with constant-time compare
- **Operator gating** — all significant actions require a Discord reaction from an authorised user ID
- **No secrets in database** — tokens read at startup, never persisted
- **Non-root container** — runs as unprivileged user

## Observability

Logs to stdout via `slog` structured text format. Health endpoints:

- `GET /health` — liveness probe
- `GET /ready` — readiness probe

All significant actions are also written to the `sentinel_actions` audit table.

## Further Documentation

- [Architecture](docs/architecture.md) — component diagram, data flows, design decisions, concurrency model
- [Development](docs/development.md) — prerequisites, build/test commands, local setup, environment variables

## License

Proprietary. All rights reserved.
