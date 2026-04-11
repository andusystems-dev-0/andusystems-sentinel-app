# Sentinel

Autonomous SDLC orchestration engine. Sentinel monitors Forgejo repositories, runs nightly code analysis via a local LLM, sanitizes secrets before mirroring to GitHub, and surfaces everything through Discord with operator-gated reactions.

## What It Does

| Mode | Trigger | What Happens |
|------|---------|--------------|
| **Mode 1 -- Nightly** | Cron (default 23:00) | Diffs Forgejo HEAD vs last run, LLM analysis, opens fix/feat/docs PRs on Forgejo, Discord notification |
| **Mode 2 -- PR Review** | Forgejo webhook (`pull_request`) | Reviews developer PRs via LLM, posts verdict + per-file notes, optionally opens housekeeping companion PR |
| **Mode 3 -- Sync** | Push webhook or manual | Sanitizes changed files, pushes clean content to GitHub mirror, Discord alert on findings |
| **Mode 4 -- Migration** | Manual (`--mode migrate`) | One-time full-repo scan, sanitizes all files, pushes to GitHub mirror, Discord confirmation flow |
| **Doc-Gen** | Manual or nightly | Generates/updates documentation via [AI_ASSISTANT] Code CLI, opens PR on Forgejo |

Operator decisions (merge, close, approve finding, reject finding) happen entirely through Discord emoji reactions. Forgejo actions taken in the UI are reflected back to Discord automatically via webhooks.

## Architecture Overview

```
Forgejo --webhook--> Sentinel --> Discord
   ^                    |
   |              +-----+------+
   |           Ollama  SQLite  GitHub
   |           (LLM)   (DB)   (mirror)
   +-- PRs <-----------+
```

- **Single SQLite database** -- all state, audit log, pending findings, PR records
- **Two git worktrees** -- one cloned from Forgejo (source of truth) and one for sanitized staging (pushed to GitHub)
- **Three-layer sanitization** -- gitleaks (L1, auto-redact) -> Ollama (L2, operator review) -> [AI_ASSISTANT] Code CLI (L3, optional safety net)
- **Per-repo RW locking** -- `sync.RWMutex` per repo prevents concurrent worktree corruption
- **Webhook async ACK** -- HTTP 200 returned immediately; processing happens in a worker pool
- **Operator-gated reactions** -- only Discord user IDs in `operator_user_ids` can trigger Forgejo actions
- **Drift reconciliation** -- periodic checks catch missed webhooks and trigger Mode 3 sync automatically
- **Web dashboard** -- embedded SvelteKit SPA served from the Go binary, with real-time SSE updates
- **REST API** -- JSON endpoints for sessions, tasks, PRs, actions, and repo config

For detailed architecture documentation, see [docs/architecture.md](docs/architecture.md).

## Quick Start

### Prerequisites

| Component | Requirement |
|-----------|-------------|
| **Go** | 1.24+ (no CGo required) |
| **Forgejo** | Running instance with two tokens: `sentinel` service account (read/write PRs, no merge) and an operator token (merge only) |
| **GitHub** | Organisation or account for mirror repos; PAT with `repo` scope |
| **Discord** | Bot application with message content and reaction intents |
| **Ollama** | Running with `qwen2.5-coder:14b` pulled; required for nightly analysis and sanitization Layer 2 |
| **Storage** | Local directory (dev) or RWO PVC (Kubernetes); SQLite requires exclusive file access |

**Optional:**

| Component | Used For |
|-----------|----------|
| **[AI_ASSISTANT] Code CLI** | Mode 1 task execution for `fix`, `feat`, `vulnerability`, `refactor` task types; sanitization Layer 3; documentation generation |
| **Node.js / npm** | Building the SvelteKit web dashboard (`make web-build`) |

### 1. Build

```bash
make build
# Binary at ./bin/sentinel

# With web dashboard:
make full-build
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

For local development, put these in a `.env` file (never committed). Sentinel loads it automatically via `godotenv`.

### 4. Initial Migration (per repo)

Run once per repo before starting the daemon:

```bash
./bin/sentinel --config config.yaml --mode migrate --repo myrepo
```

Use `--force` if the GitHub mirror already has content. Sentinel posts a confirmation message in Discord -- react to approve or cancel.

### 5. Start the Daemon

```bash
./bin/sentinel --config config.yaml
```

This starts the webhook HTTP server, Discord bot, drift reconciler, REST API, web dashboard, and nightly cron scheduler.

## Run Modes

```bash
# Full daemon (webhook server + Discord bot + REST API + dashboard + cron)
./bin/sentinel --config config.yaml

# Nightly pipeline for one repo
./bin/sentinel --config config.yaml --mode nightly --repo myrepo

# Nightly pipeline with full scan (ignores skip window / flood threshold)
./bin/sentinel --config config.yaml --mode nightly --repo myrepo --force

# Nightly pipeline for all repos
./bin/sentinel --config config.yaml --mode nightly

# Incremental sync (dry-run: no GitHub push)
./bin/sentinel --config config.yaml --mode sync --repo myrepo --dry-run

# Full migration
./bin/sentinel --config config.yaml --mode migrate --repo myrepo --force

# Documentation generation (single repo)
./bin/sentinel --config config.yaml --mode doc-gen --repo myrepo

# Documentation generation (all repos)
./bin/sentinel --config config.yaml --mode doc-gen
```

## Configuration Reference

### Environment Variables

| Variable | Required | Description |
|----------|----------|-------------|
| `FORGEJO_SENTINEL_TOKEN` | Yes | Read-only Forgejo service account token |
| `FORGEJO_OPERATOR_TOKEN` | Yes | Merge-only token; used in exactly one code path (`forge/forgejo.go:MergePR`) |
| `DISCORD_BOT_TOKEN` | Yes | Full bot token including `Bot ` prefix |
| `GITHUB_TOKEN` | Yes | PAT with `repo` scope |
| `FORGEJO_WEBHOOK_SECRET` | Yes | HMAC-SHA256 shared secret for webhook validation |
| `ANTHROPIC_API_KEY` | No | Enables sanitization Layer 3 ([AI_ASSISTANT] API) |
| `SENTINEL_DB_PATH` | No | SQLite database file path (default: `/data/db/sentinel.db`) |
| `SENTINEL_INGRESS_HOST` | No | If set, auto-registers Forgejo webhooks on all repos at startup |

### Config File Sections

The full configuration is in `config.yaml`. Key sections:

| Section | Purpose |
|---------|---------|
| `sentinel` | Git identity for commits (name, email, usernames) |
| `forgejo` | Forgejo instance base URL |
| `github` | GitHub API base URL, organisation, and mirror commit identity |
| `discord` | Guild ID, channel IDs (actions, PRs, logs, git-logs), operator user IDs |
| `pr` | Merge strategy, priority types, mention settings, housekeeping |
| `nightly` | Cron schedule, active-dev skip window, flood threshold, session budget |
| `digest` | Nightly digest formatting and collapse threshold |
| `webhook` | Listener port, queue size, worker count, review cooldown |
| `ollama` | Host, model, temperature, context window |
| `claude_api` | Model, max tokens, rate limits |
| `claude_code` | Binary path, CLI flags, task timeout |
| `worktree` | Base path for git worktrees |
| `sanitize` | Confidence thresholds, skip patterns, category reasons, scrub patterns, layer controls |
| `allowlist` | Confirmation TTL for migration approvals |
| `doc_gen` | Documentation generation settings (targets, context file limit) |
| `obsidian` | Obsidian vault integration (path, changelog/docs directories) |
| `reconcile` | Drift detection (on-startup flag, interval) |
| `repos` | Per-repo configuration (paths, languages, focus areas, sync settings, doc targets) |

See `config.yaml.example` for the complete annotated reference.

## Sanitization Pipeline

Every file flowing from Forgejo to GitHub passes through up to three layers:

```
Input file
    |
    v
[Skip zone pre-scan] -- previously approved values excluded
    |
    v
Layer 1: gitleaks + regex
  >= 0.9 confidence -> auto-redact
  < 0.9 confidence -> pass to Layer 2
    |
    v
Layer 2: Ollama (semantic analysis)
  Any finding -> pending operator review
  On timeout/error -> falls back to [AI_ASSISTANT] Code CLI
    |
    v
Layer 3: [AI_ASSISTANT] Code CLI (optional safety net, configurable)
  Any new finding -> pending operator review
    |
    v
Staged output with SENTINEL BOT tags
```

**Discord reactions for findings:**
- Approve suggested replacement
- Reject (keep original in Forgejo only)
- Provide custom replacement in thread
- Re-analyse with LLM

**Discord reactions for PRs:**
- Merge via Forgejo API
- Close without merging
- Open discussion thread

## REST API

Sentinel exposes a read-only REST API for the web dashboard and external integrations:

| Endpoint | Description |
|----------|-------------|
| `GET /api/v1/sessions` | List recent nightly sessions |
| `GET /api/v1/sessions/active` | Get currently running session |
| `GET /api/v1/sessions/{id}` | Get session by ID |
| `GET /api/v1/sessions/{id}/tasks` | List tasks for a session |
| `GET /api/v1/tasks` | List recent tasks |
| `GET /api/v1/tasks/{id}` | Get task by ID |
| `GET /api/v1/prs` | List open PRs |
| `GET /api/v1/actions` | List recent audit actions |
| `GET /api/v1/repos` | List configured repos |
| `GET /api/v1/events` | SSE stream for real-time updates |

All list endpoints accept an optional `?limit=N` query parameter (max 500).

## Web Dashboard

Sentinel includes an embedded SvelteKit single-page application that provides a real-time view of nightly sessions, tasks, PRs, and audit actions. The dashboard connects via SSE for live updates during nightly runs.

```bash
# Build the dashboard (requires Node.js)
make web-build

# Then build the Go binary with embedded assets
make build

# Or use the combined target:
make full-build
```

During development, run the SvelteKit dev server with hot reload:

```bash
make web-dev
# Proxies API requests to Go server on the webhook listener port
```

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
- **Replicas must be 1** -- SQLite requires exclusive PVC access
- **RWO volumes only** -- never use ReadWriteMany
- **ArgoCD sync is manual only** -- sentinel has side effects on external systems

## LLM Roles

| Role | Purpose |
|------|---------|
| A -- Analyst | Nightly code analysis; identifies improvement tasks |
| B -- Reviewer | Reviews developer PRs; produces verdict + per-file notes |
| C -- Prose | General prose generation (PR bodies, summaries) |
| D -- Sanitize | Semantic secret detection in file content |
| E -- Finding thread | Answers questions about findings in Discord threads |
| F -- PR thread | Answers questions about PRs in Discord threads |
| G -- Housekeeping | Generates companion PR bodies for housekeeping changes |

## Security Model

- **Token isolation** -- sentinel token has no merge permission; operator token used in exactly one code path
- **Webhook validation** -- HMAC-SHA256 with constant-time compare
- **Operator gating** -- all significant actions require a Discord reaction from an authorised user ID
- **No secrets in database** -- tokens read at startup, never persisted
- **Non-root container** -- runs as unprivileged user
- **Scrub patterns** -- configurable regex substitutions applied to all mirrored content as a final safety net

## Observability

Logs to stdout via `slog` structured text format. Health endpoints:

- `GET /health` -- liveness probe
- `GET /ready` -- readiness probe

All significant actions are also written to the `sentinel_actions` audit table and surfaced via the REST API.

## Further Documentation

- [Architecture](docs/architecture.md) -- component diagram, data flows, design decisions, concurrency model
- [Development](docs/development.md) -- prerequisites, build/test commands, local setup, environment variables

## License

Proprietary. All rights reserved.
