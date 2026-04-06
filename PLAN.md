# sentinel — Implementation Plan (v14 FINAL)

---

## 1. Project Layout

```
sentinel/
├── cmd/
│   └── sentinel/
│       └── main.go                    # Entry point: config load, DB init, mode dispatch, cron, HTTP server start
├── internal/
│   ├── config/
│   │   ├── config.go                  # Config struct definition and YAML unmarshalling
│   │   ├── env.go                     # Env var resolution and Kubernetes Secret binding
│   │   └── validate.go                # Validation: category_reason '>' check, enum checks
│   ├── sanitize/
│   │   ├── pipeline.go                # SanitizationPipeline orchestrator: layer sequencing, skip zones, confidence routing
│   │   ├── layer1_gitleaks.go         # Layer 1: gitleaks library adapter + Go regex patterns
│   │   ├── layer2_llm.go              # Layer 2: calls LLMClient.SanitizeChunk, parses findings JSON
│   │   ├── layer3_claude.go           # Layer 3: calls ClaudeAPIClient.SanitizeChunk, parses findings JSON
│   │   ├── skipzones.go               # approved_values byte-range pre-scan, SkipZone builder
│   │   ├── tag.go                     # Sentinel tag string construction from category_reasons config
│   │   └── staging.go                 # Staging content construction: single L-to-R pass, token_index assignment
│   ├── discord/
│   │   ├── bot.go                     # DiscordBot lifecycle: Start, Stop, session management
│   │   ├── embed.go                   # Embed construction helpers: color, prefix, footer, field layout
│   │   ├── reactions.go               # Reaction event router: dispatch to finding or PR reaction handlers
│   │   ├── threads.go                 # Thread open, post, Q&A routing to LLM
│   │   ├── digest.go                  # Nightly pending digest: format + post to PR channel
│   │   └── commands.go                # /sentinel command parser and dispatcher
│   ├── worktree/
│   │   ├── manager.go                 # WorktreeManager: ensure, read, write, push for both worktrees
│   │   ├── lock.go                    # ForgejoWorktreeLock: sync.RWMutex per repo behind a map+mutex
│   │   ├── token_index.go             # token_index resolution algorithm: scan, adjust, replace, error path
│   │   ├── push.go                    # GitHub staging push: per-file commit, PushAllStaging
│   │   └── filemutex.go               # FileMutexRegistry: per-(repo,filename) sync.Mutex
│   ├── prnotify/
│   │   ├── notifier.go                # PRNotifier: PostPRNotification, embed construction, reactions seed
│   │   ├── reactions.go               # ✅/❌/💬 reaction handlers for PR channel; operator token merge
│   │   ├── sync.go                    # Forgejo→Discord sync on pull_request webhook merge/close events
│   │   └── mention.go                 # @here cooldown tracker backed by sentinel_actions DB
│   ├── store/
│   │   ├── db.go                      # SQLite open, WAL pragma, schema migration runner
│   │   ├── prs.go                     # SentinelPRStore implementation
│   │   ├── findings.go                # PendingResolutionStore + SanitizationFinding store
│   │   ├── approvedvalues.go          # ApprovedValuesStore implementation
│   │   ├── syncruns.go                # SyncRun CRUD
│   │   ├── tasks.go                   # Task CRUD
│   │   ├── actions.go                 # sentinel_actions insert + query (including @here cooldown query)
│   │   ├── confirmations.go           # PendingConfirmation CRUD with TTL check
│   │   ├── reviews.go                 # PR review dedup records
│   │   ├── webhookevents.go           # Webhook event log
│   │   └── migrations.go              # SQL migration definitions as embedded strings
│   ├── forge/
│   │   ├── forgejo.go                 # ForgejoProvider implementation (gitea SDK)
│   │   ├── github.go                  # GitHub API client: mirror repo, push, webhook (go-github)
│   │   └── webhook_register.go        # Idempotent webhook registration on each watched repo
│   ├── llm/
│   │   ├── client.go                  # LLMClient implementation wrapping Ollama API
│   │   ├── batcher.go                 # Multi-call batching: partition diff by file, token estimate
│   │   ├── prompts.go                 # Prompt templates for Roles A-G (loaded at startup)
│   │   └── semaphore.go               # Global Ollama semaphore (max 1 concurrent)
│   ├── executor/
│   │   ├── claudecode.go              # TaskExecutor: os/exec → [AI_ASSISTANT] binary, stdin task spec, semaphore
│   │   ├── semaphore.go               # Global [AI_ASSISTANT] Code semaphore (max 1 concurrent)
│   │   └── template.go                # [AI_ASSISTANT] Code task spec text/template renderer
│   ├── pipeline/
│   │   ├── nightly.go                 # Mode 1 orchestrator: pre-flight, analysis, routing, branch+PR per task
│   │   ├── preflight.go               # Skip checks: active dev window, flood threshold, excluded, migration pending
│   │   ├── router.go                  # Task routing: type+complexity → executor assignment
│   │   └── dependency.go              # Go dependency bump: manifest parse, version update, commit content
│   ├── webhook/
│   │   ├── server.go                  # HTTP server: handler registration, listen, graceful shutdown
│   │   ├── handler.go                 # Webhook HTTP handler: HMAC validate, parse, enqueue, ACK 200
│   │   ├── queue.go                   # WebhookQueue: buffered channel, Enqueue/Dequeue, full→429
│   │   ├── processor.go               # Async worker pool: event dispatch to Mode 2 / push handler / sync handler
│   │   └── hmac.go                    # HMAC-SHA256 validation using constant-time compare
│   ├── sync/
│   │   ├── runner.go                  # SyncRunner: Mode 3 incremental sync orchestrator
│   │   └── changed.go                 # Identify changed files since last sync SHA via go-git
│   └── migration/
│       ├── manager.go                 # MigrationManager: Mode 4 full-repo migration
│       └── confirm.go                 # --force confirmation: post embed, await ✅ reaction with TTL
├── prompts/
│   ├── role_a_analyst.md              # Role A system prompt template (nightly analyst)
│   ├── role_b_reviewer.md             # Role B system prompt template (PR reviewer)
│   ├── role_c_prose.md                # Role C system prompt template (prose writer)
│   ├── role_d_sanitize.md             # Role D system prompt template (sanitization semantic pass)
│   ├── role_e_finding_thread.md       # Role E system prompt template (finding discussion)
│   ├── role_f_pr_thread.md            # Role F system prompt template (PR discussion)
│   └── role_g_housekeeping.md         # Role G system prompt template (housekeeping PR body)
├── charts/
│   └── sentinel/
│       ├── Chart.yaml                 # Helm chart metadata
│       ├── values.yaml                # All configurable Helm values
│       └── templates/
│           ├── deployment.yaml        # Deployment: both PVC mounts, all env vars, probes
│           ├── service.yaml           # ClusterIP service for webhook port
│           ├── configmap.yaml         # config.yaml + non-secret Discord config
│           ├── secret.yaml            # All Kubernetes Secrets (tokens, webhook secret, API key)
│           ├── pvc-forgejo.yaml       # Longhorn RWO PVC for Forgejo workspace
│           ├── pvc-github.yaml        # Longhorn RWO PVC for GitHub staging workspace
│           ├── ingressroute.yaml      # Traefik IngressRoute for webhook HTTPS endpoint
│           ├── certificate.yaml       # cert-manager Certificate with DNS-01 solver
│           └── networkpolicy.yaml     # Egress allowlist to known external hosts
├── argocd/
│   └── sentinel-app.yaml             # ArgoCD Application manifest (manual sync)
├── fixtures/
│   ├── diff_small.patch               # Small diff fixture for LLM testing
│   ├── diff_large.patch               # Large multi-file diff fixture for batching tests
│   ├── secret_file.go                 # File with embedded secrets for sanitize-test
│   └── webhook_pr_open.json           # Synthetic Forgejo pull_request webhook payload
├── Dockerfile                         # Multi-stage build: Go builder + minimal runtime image
├── Makefile                           # All build/test/dev targets
├── go.mod                             # Go module definition
├── go.sum                             # Dependency checksums
└── config.yaml.example                # Annotated example config (committed; no secrets)
```

---

## 2. Go Module & Package Structure

**Module name**: `github.com/andusystems/sentinel`

### External Dependencies

| Import Path | Version | Justification |
|---|---|---|
| `github.com/robfig/cron/v3` | v3.0.1 | Cron scheduler for nightly pipeline |
| `github.com/bwmarrin/discordgo` | v0.28.1 | Discord bot: gateway intents, reactions, threads |
| `code.gitea.io/sdk/gitea` | v0.19.0 | Forgejo (Gitea-compatible) API client |
| `github.com/google/go-github/v66` | v66.0.0 | GitHub API: mirror repo, push, rate limit |
| `github.com/go-git/go-git/v5` | v5.12.0 | Git operations: clone, pull, diff, worktree |
| `github.com/ollama/ollama/api` | v0.6.5 | Ollama LLM HTTP client |
| `modernc.org/sqlite` | v1.33.1 | Pure-Go SQLite driver (no CGo) |
| `gopkg.in/yaml.v3` | v3.0.1 | Config YAML parsing |
| `github.com/joho/godotenv` | v1.5.1 | .env file loading for local dev |
| `github.com/gitleaks/gitleaks/v8` | v8.21.2 | Secret scanning (library API) |
| `golang.org/x/time/rate` | v0.9.0 | Token bucket rate limiter for [AI_ASSISTANT] API |

### Internal Package Breakdown

**`internal/config`**
- Responsibility: Load, parse, validate all configuration. Resolve env var references.
- Public surface: `Config` struct, `Load(path string) (*Config, error)`.
- Must-NOT: make network calls, access filesystem beyond config file.

**`internal/sanitize`**
- Responsibility: Three-layer sanitization pipeline. Sentinel tag construction. Staging content assembly.
- Public surface: `SanitizationPipeline` interface impl, `NewPipeline(...)`, `Tag(category, reason string) string`.
- Must-NOT: call Ollama directly (use `LLMClient` interface), call Forgejo API, open PRs.
- **Only package that calls [AI_ASSISTANT] API** via `ClaudeAPIClient`.

**`internal/discord`**
- Responsibility: Bot lifecycle, all Discord messaging, reaction dispatch, thread management, commands.
- Public surface: `DiscordBot` interface impl, `ReactionHandler` interface.
- Must-NOT: call Forgejo API directly, make PR merge decisions.
- **Only package that owns bot session and reaction dispatch**.

**`internal/worktree`**
- Responsibility: Two-worktree model. ForgejoWorktreeLock. token_index algorithm. GitHub staging push.
- Public surface: `WorktreeManager`, `ForgejoWorktreeLock`, `FileMutexRegistry`.
- Must-NOT: call Forgejo API, call Ollama, call Discord.
- **Only package that reads/writes worktree files and pushes to GitHub**.

**`internal/prnotify`**
- Responsibility: PR notification embeds. ✅/❌/💬 reaction handlers. Forgejo↔Discord state sync.
- Public surface: `PRNotifier` interface impl.
- Must-NOT: call Ollama, write worktree files.
- **Only package that calls Forgejo merge API with operator token**.

**`internal/store`**
- Responsibility: All SQLite DB operations. Schema migrations. WAL setup.
- Public surface: All `*Store` interface impls. `Open(dsn string) (*DB, error)`.
- Must-NOT: make network calls, access filesystem beyond DB file.

**`internal/forge`**
- Responsibility: Forgejo and GitHub API call implementations. Webhook registration.
- Public surface: `ForgejoProvider` interface impl, `GitHubProvider` interface impl.
- Must-NOT: own Discord state, access worktree directly.
- **Only package that makes Forgejo/GitHub API calls**.

**`internal/llm`**
- Responsibility: Ollama API client. Multi-call batching. Prompt rendering. Global semaphore.
- Public surface: `LLMClient` interface impl.
- Must-NOT: call Forgejo API, write files, call [AI_ASSISTANT] API.
- **Only package that calls Ollama**.

**`internal/executor`**
- Responsibility: [AI_ASSISTANT] Code CLI invocation via `os/exec`. Task spec template rendering. Semaphore.
- Public surface: `TaskExecutor` interface impl.
- Must-NOT: call Ollama, call Forgejo API directly.
- **Only package that invokes [AI_ASSISTANT] Code CLI**.

**`internal/pipeline`**
- Responsibility: Mode 1 nightly SDLC orchestration. Pre-flight checks. Task routing. Dependency bumps.
- Public surface: `Run(ctx, repo string) error`.
- Must-NOT: call LLM directly (use `LLMClient`), push to Forgejo directly.

**`internal/webhook`**
- Responsibility: HTTP server. Event queue. HMAC validation. Worker pool dispatch.
- Public surface: `WebhookQueue` interface impl, `Server` (Start/Stop).
- Must-NOT: own Discord state, call Forgejo API from handlers (enqueue only).
- **Only package that owns the HTTP server and event queue**.

**`internal/sync`**
- Responsibility: Mode 3 incremental Forgejo→GitHub sync.
- Public surface: `SyncRunner` interface impl.
- Must-NOT: open Forgejo PRs.

**`internal/migration`**
- Responsibility: Mode 4 initial full-repo migration. --force confirmation flow.
- Public surface: `MigrationManager` interface impl.
- Must-NOT: make concurrent modifications to other repos.

---

## 3. LLM Routing Table

| Action | Go | Local LLM | [AI_ASSISTANT] API | [AI_ASSISTANT] Code CLI |
|---|---|---|---|---|
| Parse YAML config | ✓ | | | |
| Generate branch name | ✓ | | | |
| Generate PR title from template | ✓ | | | |
| Generate PR body (structure + interpolation) | ✓ | | | |
| Post Forgejo PR review comment (mechanics) | ✓ | | | |
| Bump dependency manifest version | ✓ | | | |
| Staging content construction (single pass) | ✓ | | | |
| token_index resolution | ✓ | | | |
| Sentinel tag string construction | ✓ | | | |
| HMAC webhook validation | ✓ | | | |
| Webhook async ACK + enqueue | ✓ | | | |
| Discord embed construction | ✓ | | | |
| Priority tier assignment | ✓ | | | |
| Forgejo↔Discord state sync (mechanics) | ✓ | | | |
| `skip_if_active_dev` git log check | ✓ | | | |
| PR dedup cooldown check | ✓ | | | |
| @here cooldown check | ✓ | | | |
| token_index error + Discord alert | ✓ | | | |
| Forgejo webhook auto-registration | ✓ | | | |
| Commit message format | ✓ | | | |
| Nightly diff partitioning + batching | ✓ | | | |
| Per-file diff extraction (go-git) | ✓ | | | |
| Token count estimate (bytes ÷ 4) | ✓ | | | |
| TaskSpec array merge + dedup | ✓ | | | |
| TaskSpec re-rank by priority | ✓ | | | |
| Housekeeping developer comment (fixed format) | ✓ | | | |
| Mode 4 --force confirmation embed post | ✓ | | | |
| Forgejo issue creation (mechanics) | ✓ | | | |
| Label/milestone assignment | ✓ | | | |
| React ✅/❌ validation (operator ID check) | ✓ | | | |
| DB read/write (all tables) | ✓ | | | |
| SyncRun status updates | ✓ | | | |
| Migration status tracking | ✓ | | | |
| Skip zone application | ✓ | | | |
| Layer 1 gitleaks scan | ✓ | | | |
| [AI_ASSISTANT] Code task spec template render | ✓ | | | |
| Nightly diff analysis (Role A) | | ✓ | | |
| PR diff review — verdict, per-file notes, security/test (Role B) | | ✓ | | |
| Identify housekeeping content from PR (Role B) | | ✓ | | |
| Issue title + body prose (Role C) | | ✓ | | |
| README section prose (Role C) | | ✓ | | |
| Dependency PR description prose (Role C) | | ✓ | | |
| Release notes prose (Role C) | | ✓ | | |
| PR notification one-paragraph summary (Role C) | | ✓ | | |
| Sanitization semantic pass — Layer 2 (Role D) | | ✓ | | |
| Finding discussion thread Q&A (Role E) | | ✓ | | |
| PR discussion thread Q&A (Role F) | | ✓ | | |
| Housekeeping PR body prose (Role G) | | ✓ | | |
| Sanitization semantic pass — Layer 3 (final safety net) | | | ✓ | |
| Re-analyze finding on 🔍 operator reaction | | | ✓ | |
| Bug fix implementation (trivial/small) | | | | ✓ |
| Vulnerability remediation | | | | ✓ |
| Feature implementation (small) | | | | ✓ |
| Refactor (small complexity) | | | | ✓ |
| Code fix identified in Mode 2 review | | | | ✓ |

---

## 4. Configuration Schema

```yaml
# config.yaml — annotated example (no secrets; all tokens from env vars)

sentinel:
  git_name: "Sentinel"
  git_email: "sentinel@andusystems.com"
  forgejo_username: "sentinel"           # Forgejo service account username
  github_username: "sentinel-bot"        # GitHub bot username for mirror repos

forgejo:
  base_url: "https://git.andusystems.com"
  # Token from env: FORGEJO_SENTINEL_TOKEN
  # Operator token from env: FORGEJO_OPERATOR_TOKEN

github:
  base_url: "https://api.github.com"
  org: "andusystems"
  # Token from env: GITHUB_TOKEN

discord:
  # Bot token from env: DISCORD_BOT_TOKEN
  guild_id: "123456789012345678"
  pr_channel_id: "123456789012345679"        # All PR notifications
  findings_channel_id: "123456789012345680"  # Sanitization findings
  command_channel_id: "123456789012345681"   # /sentinel commands
  operator_user_ids:
    - "987654321098765432"                   # Allowlist of Discord user IDs that can approve/merge

pr:
  merge_strategy: "squash"             # Default: squash | merge | rebase
  high_priority_types:
    - "code"
    - "fix"
    - "feat"
    - "vulnerability"
  mention_on_security: true
  mention_cooldown_minutes: 60         # Minimum minutes between @here mentions per repo
  housekeeping:
    enabled: true
    open_only_if_content: true         # Only open housekeeping PR if files actually changed

nightly:
  cron: "0 23 * * *"                   # Default 23:00 daily
  skip_if_active_dev_within_hours: 2   # Skip if non-sentinel commits in last N hours
  flood_threshold: 5                   # Max open sentinel PRs per repo before skipping

digest:
  enabled: true
  low_priority_collapse_threshold: 5  # If >N low-priority open PRs, show count not list

webhook:
  port: 8080
  event_queue_size: 100
  processing_workers: 4
  review_cooldown_minutes: 5           # Dedup window for PR review triggers

ollama:
  host: "http://ollama.sentinel.svc.cluster.local:11434"
  model: "qwen2.5-coder:14b"
  temperature: 0.1
  context_window: 16384                # tokens
  response_buffer_tokens: 2048         # Reserved for LLM response

claude_api:
  # API key from env: ANTHROPIC_API_KEY
  model: "[AI_ASSISTANT]-sonnet-4-6"
  max_tokens: 8192
  rpm_limit: 50
  rate_limit_buffer_ms: 200

claude_code:
  binary_path: "/usr/local/bin/[AI_ASSISTANT]"
  flags:
    - "--output-format=json"
    - "--no-interactive"
  task_timeout_minutes: 30

worktree:
  base_path: "/data/workspace"        # PVC mount point

sanitize:
  high_confidence_threshold: 0.9
  medium_confidence_threshold: 0.6
  skip_patterns:
    - "*.test"
    - "testdata/**"
    - "fixtures/**"
    - "*.example"
  category_reasons:
    SECRET: "secret or credential detected"
    API_KEY: "API key or token detected"
    PASSWORD: "password or passphrase detected"
    PRIVATE_KEY: "private key material detected"
    CONNECTION_STRING: "database or service connection string detected"
    INTERNAL_URL: "internal hostname or IP address detected"
  # Note: no '>' allowed in any reason string (validated at config load)

allowlist:
  confirmation_ttl_minutes: 10

repos:
  - name: "fleetdock"
    forgejo_path: "andusystems/fleetdock"
    github_path: "andusystems/fleetdock"
    languages: ["go", "typescript"]
    focus_areas: ["security", "error-handling", "performance"]
    max_tasks_per_run: 10
    merge_strategy: "squash"           # Per-repo override
    sync_enabled: true
    excluded: false
  - name: "infra-charts"
    forgejo_path: "andusystems/infra-charts"
    github_path: "andusystems/infra-charts"
    languages: ["yaml", "helm"]
    focus_areas: ["security", "correctness"]
    max_tasks_per_run: 5
    sync_enabled: true
    excluded: false

excluded_repos: []                     # Repos to skip entirely (by name)
```

---

## 5. Database Schema

> **Critical constraint**: The SQLite database file MUST be placed on a **Longhorn RWO (ReadWriteOnce) PVC**. SQLite requires exclusive file-level access. Using an RWX PVC with multiple nodes mounting the volume simultaneously will corrupt the database. The Helm chart enforces `accessModes: [ReadWriteOnce]` and `replicas: 1`.

```sql
PRAGMA journal_mode=WAL;
PRAGMA synchronous=NORMAL;
PRAGMA busy_timeout=5000;
PRAGMA foreign_keys=ON;

-- Sentinel-authored PRs on Forgejo
CREATE TABLE sentinel_prs (
    id                    TEXT PRIMARY KEY,
    repo                  TEXT NOT NULL,
    pr_number             INTEGER NOT NULL,
    pr_url                TEXT NOT NULL,
    branch                TEXT NOT NULL,
    base_branch           TEXT NOT NULL,
    title                 TEXT NOT NULL,
    pr_type               TEXT NOT NULL,
    priority_tier         TEXT NOT NULL DEFAULT 'low',
    related_pr_number     INTEGER,
    discord_message_id    TEXT NOT NULL,
    discord_channel_id    TEXT NOT NULL,
    discord_thread_id     TEXT,
    status                TEXT NOT NULL DEFAULT 'open',
    opened_at             DATETIME NOT NULL,
    resolved_at           DATETIME,
    resolved_by           TEXT,
    task_id               TEXT,
    UNIQUE(repo, pr_number)
);
CREATE INDEX idx_sentinel_prs_status ON sentinel_prs(status);
CREATE INDEX idx_sentinel_prs_discord_msg ON sentinel_prs(discord_message_id);

-- Individual sanitization findings per file per sync run
CREATE TABLE sanitization_findings (
    id                    TEXT PRIMARY KEY,
    sync_run_id           TEXT NOT NULL REFERENCES sync_runs(id),
    layer                 INTEGER NOT NULL,         -- 1, 2, or 3
    repo                  TEXT NOT NULL,
    filename              TEXT NOT NULL,
    line_number           INTEGER NOT NULL,
    byte_offset_start     INTEGER NOT NULL,
    byte_offset_end       INTEGER NOT NULL,
    original_value        TEXT NOT NULL,            -- Never logged; read only inside per-file mutex
    suggested_replacement TEXT NOT NULL,
    category              TEXT NOT NULL,
    confidence            TEXT NOT NULL,            -- 'high' | 'medium' | 'low'
    auto_redacted         BOOLEAN NOT NULL DEFAULT FALSE,
    token_index           INTEGER,                  -- NULL for auto-redacted (high confidence)
    pending_resolution_id TEXT REFERENCES pending_resolutions(id)
);
CREATE INDEX idx_findings_repo_file ON sanitization_findings(repo, filename);
CREATE INDEX idx_findings_sync_run ON sanitization_findings(sync_run_id);

-- Pending operator decisions on medium/low confidence findings
CREATE TABLE pending_resolutions (
    id                    TEXT PRIMARY KEY,
    repo                  TEXT NOT NULL,
    filename              TEXT NOT NULL,
    finding_id            TEXT NOT NULL REFERENCES sanitization_findings(id),
    sync_run_id           TEXT NOT NULL REFERENCES sync_runs(id),
    forgejo_issue_number  INTEGER,
    discord_message_id    TEXT NOT NULL,
    discord_channel_id    TEXT NOT NULL,
    discord_thread_id     TEXT,
    has_thread            BOOLEAN NOT NULL DEFAULT FALSE,
    suggested_replacement TEXT NOT NULL,
    status                TEXT NOT NULL DEFAULT 'pending',
    superseded_by         TEXT REFERENCES pending_resolutions(id),
    resolved_at           DATETIME,
    resolved_by           TEXT,
    final_value           TEXT
);
CREATE INDEX idx_resolutions_discord_msg ON pending_resolutions(discord_message_id);
CREATE INDEX idx_resolutions_repo_file ON pending_resolutions(repo, filename, status);

-- Per-repo approved values allowlist
CREATE TABLE approved_values (
    id          TEXT PRIMARY KEY,
    repo        TEXT NOT NULL,
    value       TEXT NOT NULL,
    category    TEXT NOT NULL,
    approved_by TEXT NOT NULL,
    approved_at DATETIME NOT NULL,
    UNIQUE(repo, value)
);

-- Sync run records (Modes 3 and 4)
CREATE TABLE sync_runs (
    id                   TEXT PRIMARY KEY,
    repo                 TEXT NOT NULL,
    mode                 INTEGER NOT NULL,          -- 3 or 4
    status               TEXT NOT NULL,             -- 'running' | 'complete' | 'complete_with_pending' | 'failed'
    started_at           DATETIME NOT NULL,
    completed_at         DATETIME,
    files_synced         INTEGER DEFAULT 0,
    files_with_pending   INTEGER DEFAULT 0,
    findings_high        INTEGER DEFAULT 0,
    findings_medium      INTEGER DEFAULT 0,
    findings_low         INTEGER DEFAULT 0
);

-- Pending operator confirmations (allowlist, --force migration)
CREATE TABLE pending_confirmations (
    id                 TEXT PRIMARY KEY,
    kind               TEXT NOT NULL,               -- 'allowlist' | 'force_migration'
    repo               TEXT NOT NULL,
    value              TEXT,
    discord_message_id TEXT NOT NULL,
    discord_channel_id TEXT NOT NULL,
    requested_by       TEXT NOT NULL,
    created_at         DATETIME NOT NULL,
    expires_at         DATETIME NOT NULL,
    status             TEXT NOT NULL DEFAULT 'pending'   -- 'pending' | 'confirmed' | 'rejected' | 'expired'
);

-- Analysis task records (one per [AI_ASSISTANT] Code or LLM task)
CREATE TABLE tasks (
    id              TEXT PRIMARY KEY,
    repo            TEXT NOT NULL,
    pipeline_run_id TEXT,
    task_type       TEXT NOT NULL,
    complexity      TEXT NOT NULL,
    title           TEXT NOT NULL,
    description     TEXT NOT NULL,
    affected_files  TEXT NOT NULL,   -- JSON array
    acceptance      TEXT NOT NULL,   -- JSON array
    branch          TEXT,
    executor        TEXT NOT NULL,   -- 'claude_code' | 'llm' | 'go'
    status          TEXT NOT NULL DEFAULT 'pending',
    created_at      DATETIME NOT NULL,
    started_at      DATETIME,
    completed_at    DATETIME,
    pr_number       INTEGER
);
CREATE INDEX idx_tasks_repo_status ON tasks(repo, status);

-- Dedup records for PR reviews
CREATE TABLE pr_dedup (
    id            TEXT PRIMARY KEY,
    repo          TEXT NOT NULL,
    pr_number     INTEGER NOT NULL,
    reviewed_at   DATETIME NOT NULL,
    UNIQUE(repo, pr_number)
);

-- PR review records (Mode 2 results)
CREATE TABLE reviews (
    id              TEXT PRIMARY KEY,
    repo            TEXT NOT NULL,
    pr_number       INTEGER NOT NULL,
    verdict         TEXT NOT NULL,    -- 'APPROVE' | 'REQUEST_CHANGES' | 'COMMENT'
    comment_posted  BOOLEAN NOT NULL DEFAULT FALSE,
    housekeeping_pr INTEGER,
    fix_pr          INTEGER,
    reviewed_at     DATETIME NOT NULL
);

-- Per-file sync records (last synced SHA per file per repo)
CREATE TABLE sync_records (
    id           TEXT PRIMARY KEY,
    repo         TEXT NOT NULL,
    filename     TEXT NOT NULL,
    last_sha     TEXT NOT NULL,
    synced_at    DATETIME NOT NULL,
    UNIQUE(repo, filename)
);

-- Per-repo latest synced Forgejo HEAD SHA (sync baseline)
CREATE TABLE repo_sync_state (
    repo          TEXT PRIMARY KEY,
    forgejo_sha   TEXT NOT NULL,
    synced_at     DATETIME NOT NULL
);

-- All incoming webhook events (audit log)
CREATE TABLE webhook_events (
    id            TEXT PRIMARY KEY,
    event_type    TEXT NOT NULL,
    repo          TEXT NOT NULL,
    payload       BLOB NOT NULL,
    hmac_valid    BOOLEAN NOT NULL,
    received_at   DATETIME NOT NULL,
    processed_at  DATETIME,
    error         TEXT
);

-- All sentinel-originated actions (audit trail)
CREATE TABLE sentinel_actions (
    id            TEXT PRIMARY KEY,
    action_type   TEXT NOT NULL,   -- e.g. 'pr_created', 'pr_merged', 'discord_mention_here', 'github_push', 'claude_code_invoked'
    repo          TEXT NOT NULL,
    entity_id     TEXT,            -- PR number, finding ID, etc.
    detail        TEXT,            -- JSON metadata (no secrets)
    actor         TEXT NOT NULL DEFAULT 'sentinel',
    created_at    DATETIME NOT NULL
);
CREATE INDEX idx_actions_type_repo_created ON sentinel_actions(action_type, repo, created_at);

-- Migration state per repo
CREATE TABLE migration_status (
    repo            TEXT PRIMARY KEY,
    status          TEXT NOT NULL DEFAULT 'pending',  -- 'pending' | 'in_progress' | 'complete' | 'failed'
    forgejo_sha     TEXT,
    started_at      DATETIME,
    completed_at    DATETIME,
    error           TEXT
);
```

---

## 6. Core Go Interfaces

```go
package types

import (
    "context"
    "time"
)

// ForgejoWorktreeLock provides RW locking on the Forgejo worktree per repo.
// Write lock: git pull, branch creation, [AI_ASSISTANT] Code invocation.
// Read lock: diff reads for LLM analysis, Mode 2 review.
type ForgejoWorktreeLock interface {
    RLock(repo string)
    RUnlock(repo string)
    Lock(repo string)
    Unlock(repo string)
}

// WorktreeManager owns both worktree representations.
type WorktreeManager interface {
    EnsureForgejoWorktree(ctx context.Context, repo string) error
    ReadForgejoFile(ctx context.Context, repo, filename string) ([]byte, error)
    WriteGitHubStaging(ctx context.Context, repo, filename string, content []byte) error
    ReadGitHubStaging(ctx context.Context, repo, filename string) ([]byte, error)
    // ResolveTag — caller MUST hold FileMutexRegistry lock for (repo, filename)
    ResolveTag(ctx context.Context, repo, filename string,
        tokenIndex, resolvedCount int, finalValue string) error
    // PushStagingFile — caller MUST hold FileMutexRegistry lock for (repo, filename)
    PushStagingFile(ctx context.Context, repo, filename, commitMsg string) error
    PushAllStaging(ctx context.Context, repo, commitMsg string) error
    SentinelTag(category string) string
}

// WebhookQueue decouples HTTP ACK from async event processing.
type WebhookQueue interface {
    Enqueue(event ForgejoEvent) error   // Returns error if queue full (caller returns HTTP 429)
    Dequeue(ctx context.Context) (<-chan ForgejoEvent, error)
}

// PRNotifier manages PR notification embeds and Forgejo↔Discord sync.
type PRNotifier interface {
    PostPRNotification(ctx context.Context, pr SentinelPR,
        summary string) (messageID string, err error)
    HandleApprove(ctx context.Context, pr *SentinelPR, userID string) error
    HandleClose(ctx context.Context, pr *SentinelPR, userID string) error
    HandleDiscuss(ctx context.Context, pr *SentinelPR) error
    // HandleForgejoResolution handles both merge and close from Forgejo UI.
    HandleForgejoResolution(ctx context.Context, repo string,
        prNumber int, merged bool) error
}

// PRCreator handles sentinel branch and PR lifecycle on Forgejo.
type PRCreator interface {
    CreateBranch(ctx context.Context, repo, branchName string) error
    CommitAndPush(ctx context.Context, repo, branch, commitMsg string,
        files map[string][]byte) error
    OpenPR(ctx context.Context, opts OpenPROptions) (int, string, error)
}

type OpenPROptions struct {
    Repo            string
    Branch          string
    BaseBranch      string
    Title           string
    Body            string
    Labels          []string
    PRType          string
    PriorityTier    string
    RelatedPRNumber int
}

// SentinelPRStore manages sentinel PR lifecycle.
type SentinelPRStore interface {
    Create(ctx context.Context, pr SentinelPR) error
    GetByMessageID(ctx context.Context, messageID string) (*SentinelPR, error)
    GetByPRNumber(ctx context.Context, repo string, prNumber int) (*SentinelPR, error)
    GetOpenPRs(ctx context.Context) ([]SentinelPR, error)
    GetOpenPRsForRepo(ctx context.Context, repo string) ([]SentinelPR, error)
    MarkMerged(ctx context.Context, id, resolvedBy string) error
    MarkClosed(ctx context.Context, id, resolvedBy string) error
    SetThread(ctx context.Context, id, threadID string) error
}

// SanitizationPipeline orchestrates all three layers.
type SanitizationPipeline interface {
    SanitizeFile(ctx context.Context, opts SanitizeFileOpts) (*SanitizeFileResult, error)
    ReanalyzeFile(ctx context.Context, opts SanitizeFileOpts) (*SanitizeFileResult, error)
}

type SanitizeFileOpts struct {
    Repo      string
    Filename  string
    Content   []byte
    SkipZones []SkipZone
    SyncRunID string
}

type SanitizeFileResult struct {
    SanitizedContent []byte
    Findings         []SanitizationFinding
}

// LLMClient wraps Ollama for all LLM roles.
type LLMClient interface {
    Analyze(ctx context.Context, opts AnalyzeOpts) ([]TaskSpec, error)
    ReviewPR(ctx context.Context, opts ReviewOpts) (*ReviewResult, error)
    WriteProse(ctx context.Context, opts ProseOpts) (string, error)
    SanitizeChunk(ctx context.Context, content string) ([]SanitizationFinding, error)
    AnswerThread(ctx context.Context, opts ThreadOpts) (string, error)
    WriteHousekeepingBody(ctx context.Context, opts HousekeepingOpts) (string, error)
}

type AnalyzeOpts struct {
    Repo       string
    FileBatch  []FileDiff   // One batch <= context_window - response_buffer tokens
    FocusAreas []string
}

type ReviewOpts struct {
    Repo       string
    PRNumber   int
    PRTitle    string
    Diff       string
    BaseBranch string
}

type ProseOpts struct {
    Role    string   // 'issue', 'readme', 'dependency_pr', 'release_notes', 'pr_summary'
    Context string
}

type ThreadOpts struct {
    Role     string   // 'finding' or 'pr'
    Context  string   // Finding detail + file chunk, or PR title + diff summary
    Question string
}

type HousekeepingOpts struct {
    PRTitle           string
    DiffSummary       string
    HousekeepingFiles []string
}

// ClaudeAPIClient — sanitization only, not code authoring.
type ClaudeAPIClient interface {
    SanitizeChunk(ctx context.Context, content string) ([]SanitizationFinding, error)
}

// PendingResolutionStore manages sanitization finding lifecycle.
type PendingResolutionStore interface {
    Create(ctx context.Context, r PendingResolution) error
    GetByMessageID(ctx context.Context, messageID string) (*PendingResolution, error)
    GetPendingForFile(ctx context.Context, repo, filename string) ([]PendingResolution, error)
    CountResolvedPredecessors(ctx context.Context, repo, filename string,
        tokenIndex int) (int, error)
    Approve(ctx context.Context, id, userID, finalValue string) error
    Reject(ctx context.Context, id, userID string) error
    CustomReplace(ctx context.Context, id, userID, token string) error
    MarkReanalyzing(ctx context.Context, id string) error
    Supersede(ctx context.Context, oldID, newID string) error
    SetThread(ctx context.Context, id, threadID string) error
}

// FileMutexRegistry provides per-(repo, filename) mutexes.
type FileMutexRegistry interface {
    Lock(repo, filename string)
    Unlock(repo, filename string)
}

// ApprovedValuesStore manages per-repo safe-value allowlist.
type ApprovedValuesStore interface {
    Add(ctx context.Context, repo, value, category, approvedBy string) error
    Contains(ctx context.Context, repo, value string) (bool, error)
    GetSkipZones(ctx context.Context, repo string, content []byte) ([]SkipZone, error)
    List(ctx context.Context, repo string) ([]ApprovedValue, error)
}

// DiscordBot manages bot lifecycle and all messaging.
type DiscordBot interface {
    Start(ctx context.Context) error
    Stop() error
    PostFinding(ctx context.Context, r PendingResolution,
        f SanitizationFinding) (messageID string, err error)
    SeedFindingReactions(ctx context.Context, channelID, messageID string) error
    SeedPRReactions(ctx context.Context, channelID, messageID string) error
    EditFindingFooter(ctx context.Context, channelID, messageID, footer string) error
    EditPRFooter(ctx context.Context, channelID, messageID, footer string) error
    PostChannelMessage(ctx context.Context, channelID, content string) error
    OpenThread(ctx context.Context, channelID, messageID, name string) (string, error)
    PostInThread(ctx context.Context, threadID, content string) error
    PostNightlyDigest(ctx context.Context, digest NightlyDigest) error
}

// ReactionHandler processes one emoji reaction type (findings or PRs).
type ReactionHandler interface {
    Emoji() string
    Handle(ctx context.Context, messageID, userID string) error
}

// SyncRunner handles Mode 3.
type SyncRunner interface {
    Sync(ctx context.Context, repo string) error
}

// MigrationManager handles Mode 4.
type MigrationManager interface {
    Migrate(ctx context.Context, repo string, force bool) error
    Status(ctx context.Context, repo string) (*MigrationState, error)
}

// TaskExecutor invokes [AI_ASSISTANT] Code CLI.
type TaskExecutor interface {
    Execute(ctx context.Context, spec TaskSpec, branch, repo string) (*TaskResult, error)
}

// ForgejoProvider wraps the Forgejo API.
type ForgejoProvider interface {
    GetPRDiff(ctx context.Context, repo string, prNumber int) (string, error)
    CreatePR(ctx context.Context, opts OpenPROptions) (int, string, error)
    CreateBranch(ctx context.Context, repo, name, fromSHA string) error
    MergePR(ctx context.Context, repo string, prNumber int,
        strategy, token string) error
    ClosePR(ctx context.Context, repo string, prNumber int) error
    CreateIssue(ctx context.Context, repo string, opts IssueOptions) (int, error)
    PostPRComment(ctx context.Context, repo string, prNumber int, body string) error
    PostReview(ctx context.Context, repo string, prNumber int,
        verdict, body string) error
    ListOpenPRs(ctx context.Context, repo string) ([]ForgejoPR, error)
    GetWebhookEvents(ctx context.Context, repo string) ([]string, error)
    RegisterWebhook(ctx context.Context, repo, url, secret string,
        events []string) error
}
```

---

## 7. Key Data Structures

```go
package types

import "time"

type SentinelPR struct {
    ID               string
    Repo             string
    PRNumber         int
    PRUrl            string
    Branch           string
    BaseBranch       string
    Title            string
    PRType           string
    PriorityTier     PRPriorityTier
    RelatedPRNumber  int
    DiscordMessageID string
    DiscordChannelID string
    DiscordThreadID  string
    Status           PRStatus
    OpenedAt         time.Time
    ResolvedAt       *time.Time
    ResolvedBy       string
    TaskID           string
}

type PRStatus string

const (
    PRStatusOpen   PRStatus = "open"
    PRStatusMerged PRStatus = "merged"
    PRStatusClosed PRStatus = "closed"
)

type PRPriorityTier string

const (
    PRTierHigh PRPriorityTier = "high"
    PRTierLow  PRPriorityTier = "low"
)

type ForgejoEvent struct {
    Type       string    // "pull_request" | "push" | "issue_comment"
    Repo       string
    Payload    []byte
    ReceivedAt time.Time
}

type NightlyDigest struct {
    HighPriorityPRs []SentinelPR
    LowPriorityPRs  []SentinelPR
    PendingFindings []PendingFindingDigest
}

type PendingFindingDigest struct {
    Repo          string
    Count         int
    AffectedFiles []string
    OldestAge     time.Duration
}

type SanitizationFinding struct {
    ID                   string
    SyncRunID            string
    Layer                int
    Repo                 string
    Filename             string
    LineNumber           int
    ByteOffsetStart      int
    ByteOffsetEnd        int
    OriginalValue        string   // Never logged; accessed only inside per-file mutex
    SuggestedReplacement string
    Category             string
    Confidence           string   // "high" | "medium" | "low"
    AutoRedacted         bool
    TokenIndex           int      // 0 for auto-redacted
    PendingResolutionID  string
}

type PendingResolution struct {
    ID                   string
    Repo                 string
    Filename             string
    FindingID            string
    SyncRunID            string
    ForgejoIssueNumber   int
    DiscordMessageID     string
    DiscordChannelID     string
    DiscordThreadID      string
    HasThread            bool
    SuggestedReplacement string
    Status               ResolutionStatus
    SupersededBy         string
    ResolvedAt           *time.Time
    ResolvedBy           string
    FinalValue           string
}

type ResolutionStatus string

const (
    StatusPending        ResolutionStatus = "pending"
    StatusApproved       ResolutionStatus = "approved"
    StatusRejected       ResolutionStatus = "rejected"
    StatusCustomReplaced ResolutionStatus = "custom_replaced"
    StatusReanalyzing    ResolutionStatus = "reanalyzing"
    StatusSuperseded     ResolutionStatus = "superseded"
)

type SkipZone struct{ Start, End int }

type ApprovedValue struct {
    ID         string
    Repo       string
    Value      string
    Category   string
    ApprovedBy string
    ApprovedAt time.Time
}

type TaskSpec struct {
    ID                 string
    Type               string   // "bug" | "vulnerability" | "docs" | "dependency-update" | "feature" | "refactor" | "issue"
    Priority           string   // "1" (highest) through "5"
    Complexity         string   // "trivial" | "small" | "medium" | "large"
    Title              string
    AffectedFiles      []string
    Description        string
    AcceptanceCriteria []string
    ContextNotes       string
}

type TaskResult struct {
    Success bool
    Output  string
    Error   string
}

type ReviewResult struct {
    Verdict            string   // "APPROVE" | "REQUEST_CHANGES" | "COMMENT"
    PerFileNotes       []FileNote
    SecurityAssessment string
    TestAssessment     string
    ChangelogText      string
    DocUpdates         map[string]string // filename → updated content
    HousekeepingFiles  map[string][]byte // filename → content for companion PR
    IssueSpecs         []IssueSpec
    CodeFixNeeded      bool
    CodeFixDescription string
}

type FileNote struct {
    Filename string
    Notes    string
    Severity string   // "info" | "warning" | "critical"
}

type IssueSpec struct {
    Title  string
    Body   string
    Labels []string
}

type IssueOptions struct {
    Title  string
    Body   string
    Labels []string
}

type ForgejoPR struct {
    Number   int
    Title    string
    URL      string
    HeadRef  string
    BaseRef  string
    State    string
    MergedAt *time.Time
}

type FileDiff struct {
    Filename     string
    Diff         string
    LinesAdded   int
    LinesRemoved int
    EstTokens    int
}

type MigrationState struct {
    Repo        string
    Status      string
    ForgejoSHA  string
    StartedAt   *time.Time
    CompletedAt *time.Time
    Error       string
}
```

---

## 8. Local LLM Prompt Design

### Role A — Nightly Analyst

**System prompt** (`prompts/role_a_analyst.md`):
```
You are a code analysis assistant. You analyze git diffs and identify concrete improvement tasks.

RULES:
- Output ONLY valid JSON. No prose before or after the JSON array.
- Each task must be actionable by a developer or automated tool.
- Focus on issues in the diff, not general repo health.
- Security vulnerabilities always receive priority "1".
- Do not invent tasks not evidenced by the diff.
- Complexity scale: trivial (< 10 lines), small (10-50), medium (50-200), large (> 200).
- If a finding is too large for automated fix, use complexity "large" and type "issue".

Focus areas provided by operator (weight these higher): {{.FocusAreas}}

RESPONSE SCHEMA (JSON array of objects):
[
  {
    "type": "bug|vulnerability|docs|dependency-update|feature|refactor|issue|changelog|readme",
    "priority": "1|2|3|4|5",
    "complexity": "trivial|small|medium|large",
    "title": "Short imperative title (< 80 chars)",
    "affected_files": ["path/to/file.go"],
    "description": "2-4 sentences describing the problem and suggested approach.",
    "acceptance_criteria": ["Criterion 1", "Criterion 2"],
    "context_notes": "Any relevant context from the diff"
  }
]
```

**User prompt construction**: For each file batch:
```
Repo: {{.Repo}}
Files in this batch: {{range .Files}}{{.Filename}} (+{{.LinesAdded}}/-{{.LinesRemoved}})
{{end}}

Diffs:
{{range .Files}}
--- {{.Filename}} ---
{{.Diff}}

{{end}}

Return a JSON array of tasks found in these files.
```

**Context batching**: Go partitions files into batches where `sum(EstTokens) <= context_window - response_buffer_tokens`. Focus-area files go first. Files sorted by `LinesAdded + LinesRemoved` descending within each priority group.

**Response handling**: Unmarshal JSON. Validate each item: `type` in allowed enum, `priority` "1"-"5", `complexity` in allowed enum. Skip invalid items (log warning). Non-JSON response: retry once. Second failure: log error, skip batch.

**Retry**: Max 1 retry per batch. On second failure: record error in `sentinel_actions`, continue to next batch.

---

### Role B — PR Reviewer

**System prompt** (`prompts/role_b_reviewer.md`):
```
You are a code review assistant. You review pull request diffs and produce structured JSON feedback.

RULES:
- Output ONLY valid JSON matching the schema below.
- Verdict must be: APPROVE, REQUEST_CHANGES, or COMMENT.
- Security issues always result in REQUEST_CHANGES.
- Housekeeping files: CHANGELOG, README, docs updates that are missing or outdated.
- Only flag code fixes if there is a concrete, implementable fix (not style opinions).
- Do not invent issues not evidenced by the diff.

RESPONSE SCHEMA:
{
  "verdict": "APPROVE|REQUEST_CHANGES|COMMENT",
  "per_file_notes": [
    {"filename": "...", "notes": "...", "severity": "info|warning|critical"}
  ],
  "security_assessment": "One paragraph on security posture.",
  "test_assessment": "One paragraph on test coverage.",
  "changelog_text": "Markdown entry for CHANGELOG.md (empty string if not applicable).",
  "doc_updates": {"docs/api.md": "Updated section text"},
  "housekeeping_files": {"CHANGELOG.md": "Full updated file content"},
  "issue_specs": [
    {"title": "...", "body": "...", "labels": ["..."]}
  ],
  "code_fix_needed": false,
  "code_fix_description": ""
}
```

**User prompt**:
```
Repo: {{.Repo}}  PR: #{{.PRNumber}} — {{.PRTitle}}
Base branch: {{.BaseBranch}}

Diff:
{{.Diff}}

Produce a JSON review result.
```

**Context management**: If diff exceeds token budget, make multiple calls — one per file group. Merge `per_file_notes`. Take most severe `verdict`. Concatenate assessments. Merge maps. Deduplicate `issue_specs` by title.

**Response handling**: Validate `verdict` enum. Invalid JSON → retry once → on second failure, post Forgejo comment: "Automated review failed to produce structured output. Manual review required."

---

### Role C — Prose Writer

**System prompt** (`prompts/role_c_prose.md`):
```
You are a technical writer. You write concise, clear prose for software project artifacts.
Output only the requested text. No preamble, no explanation, no JSON.
Match the tone: professional, direct, developer-focused.
```

**User prompt by sub-role**:
- **Issue**: `Write a Forgejo issue title and body for the following finding:\n\n{{.Context}}\n\nFormat: Title on line 1, blank line, then body.`
- **README**: `Write a README section about:\n\n{{.Context}}\n\nUse Markdown. 2-4 paragraphs.`
- **Dependency PR**: `Write a 2-sentence PR description for bumping {{.Dep}} from {{.Old}} to {{.New}} in {{.Repo}}. Note: {{.Context}}`
- **PR summary**: `Write a single concise paragraph summarizing this pull request for a Discord notification:\n\n{{.Context}}`

**Response handling**: Raw string output. Trim whitespace. Retry once on timeout or empty response. Second failure: use static fallback (`"Automated prose generation unavailable."`).

---

### Role D — Sanitization Semantic Pass

**System prompt** (`prompts/role_d_sanitize.md`):
```
You are a security assistant analyzing source code for sensitive values.
Output ONLY valid JSON. No prose.

Identify values that are:
- Secrets, tokens, API keys, passwords, private key material
- Internal hostnames, IPs, or connection strings not suitable for public repos

DO NOT flag:
- Public library names or import paths
- Example/placeholder values (e.g. "example.com", "your-token-here")
- Environment variable names (flag only their values if hardcoded)
- Values already marked with <REMOVED BY SENTINEL BOT: ...>

RESPONSE SCHEMA:
[
  {
    "line_number": 42,
    "byte_offset_start": 1200,
    "byte_offset_end": 1240,
    "original_value": "the sensitive value",
    "category": "SECRET|API_KEY|PASSWORD|PRIVATE_KEY|CONNECTION_STRING|INTERNAL_URL",
    "confidence": "high|medium|low",
    "reason": "Brief reason (< 100 chars, no > character)"
  }
]
```

**User prompt**:
```
File: {{.Filename}} (repo: {{.Repo}})

Content (post-Layer-1):
{{.Content}}

Identify any remaining sensitive values not already redacted. Return JSON array.
```

**Context management**: If file content exceeds token budget, split into overlapping chunks (200-line overlap). Merge results, deduplicate by `byte_offset_start`. Verify offsets within content bounds — discard out-of-bounds (hallucination prevention).

**Response handling**: Validate `category` and `confidence` enums. Skip malformed items. Retry once on malformed JSON.

---

### Role E — Finding Discussion Thread Responder

**System prompt** (`prompts/role_e_finding_thread.md`):
```
You are a security assistant helping an operator understand a sanitization finding.
Be concise and factual. Do not speculate beyond the evidence.
Do not make decisions — the operator decides whether to approve or reject.
Provide context, explain why the value was flagged, and answer the question directly.
```

**User prompt**:
```
Finding: {{.Category}} in {{.Filename}}:{{.LineNumber}}
Reason it was flagged: {{.Reason}}
Suggested replacement: {{.SuggestedReplacement}}

File context (surrounding lines):
{{.ContentChunk}}

Operator question: {{.Question}}
```

**Response handling**: Raw text. Post directly into Discord thread. On failure, post: "Unable to answer at this time. Please review the finding directly."

---

### Role F — PR Discussion Thread Responder

**System prompt** (`prompts/role_f_pr_thread.md`):
```
You are a code review assistant helping an operator understand a pull request.
Be concise and factual. You do not make merge decisions — the operator does.
Provide technical context, explain tradeoffs, and answer the question directly.
```

**User prompt**:
```
PR: {{.PRTitle}} in {{.Repo}}
Branch: {{.Branch}} → {{.BaseBranch}}

Diff summary:
{{.DiffSummary}}

Operator question: {{.Question}}
```

**Response handling**: Raw text. Post in Discord thread. On failure, post: "Unable to answer at this time."

---

### Role G — Housekeeping PR Body

**System prompt** (`prompts/role_g_housekeeping.md`):
```
You are a technical writer. Write a 2-3 sentence pull request description.
Explain what housekeeping was done and why, in relation to the original PR.
Be direct. No preamble. Output only the description text.
```

**User prompt**:
```
Original PR: {{.PRTitle}}
Diff summary: {{.DiffSummary}}
Housekeeping files changed: {{range .HousekeepingFiles}}- {{.}}
{{end}}

Write a 2-3 sentence PR body.
```

**Response handling**: Raw text. Trim. On failure, use fallback: `"Housekeeping updates associated with PR #{{.N}}: documentation and changelog maintenance."`

---

## 9. Go-Owned Operations — Implementation Spec

### Multi-Call LLM Analysis (Mode 1)

```
1. Use go-git to extract per-file diffs from HEAD vs last-synced SHA
2. For each file diff, estimate tokens: len(diff_bytes) / 4
3. Sort files: focus-area matches first (any focus_area keyword in filename or diff),
   then by (LinesAdded + LinesRemoved) descending
4. Greedily pack files into batches:
   current_batch_tokens = 0
   for each file:
     if current_batch_tokens + file.EstTokens > context_window - response_buffer_tokens:
       emit current batch, start new batch
     else:
       add file to current batch
5. For each batch: LLMClient.Analyze(ctx, AnalyzeOpts{FileBatch: batch})
6. Merge all returned TaskSpec arrays
7. Deduplicate: for same (AffectedFiles[0], Type), keep the one with lower priority number
8. Sort by priority ascending (1 = highest)
9. Truncate to max_tasks_per_run
```

### Webhook Async Processing

```
HTTP handler (runs in net/http goroutine):
1. Read body (max 10 MB)
2. Validate HMAC-SHA256 (hmac.Equal, constant-time): on fail → HTTP 403, log source IP
3. Parse X-Gitea-Event header → event type
4. Parse repo name from payload (JSON field: repository.name)
5. Construct ForgejoEvent{Type, Repo, Payload, ReceivedAt: time.Now()}
6. WebhookQueue.Enqueue(event):
   - If queue full: log warning, return HTTP 429
   - Success: return HTTP 200 immediately

Worker goroutines (N=config.webhook.processing_workers):
1. for event := range queue.Dequeue(ctx):
     switch event.Type:
     case "pull_request": dispatchPREvent(ctx, event)
     case "push":         dispatchPushEvent(ctx, event)
     case "issue_comment": dispatchCommentEvent(ctx, event)
```

### token_index Resolution Algorithm

```
Given: file content (GitHub staging), tokenIndex (1-based), resolvedCount (predecessors resolved)

1. Scan content byte-by-byte for occurrences of "<REMOVED BY SENTINEL BOT:"
2. Collect all start positions, sorted ascending
3. total_tags := len(positions)
4. If total_tags < tokenIndex:
   → Error: "expected at least tokenIndex tags, found total_tags in filename"
   → Post Discord alert (findings channel)
   → Return error (do not modify file)
5. targetPos := tokenIndex - 1 - resolvedCount
   (resolvedCount = number of predecessors with tokenIndex < this tokenIndex that are resolved)
   (Predecessors reduce the index because resolved tags are no longer placeholder text)
6. If targetPos < 0 or targetPos >= total_tags:
   → Error: "targetPos out of range"
   → Post Discord alert, return error
7. tagStart := positions[targetPos]
8. Find tagEnd: scan forward from tagStart for '>' character
9. Replace bytes [tagStart, tagEnd] with finalValue
10. Write modified content back to GitHub staging file
11. Return nil
```

**Edge cases**:
- `>` in reason string: blocked at config load by validation. Never reaches token_index.
- Concurrent resolution: serialized by `FileMutexRegistry.Lock(repo, filename)` held by caller.
- Tag not found where expected: error + Discord alert, no partial write.

### Sentinel Tag String

```go
func SentinelTag(category string, reasons map[string]string) string {
    reason, ok := reasons[category]
    if !ok {
        reason = "sensitive value detected"
    }
    return fmt.Sprintf("<REMOVED BY SENTINEL BOT: %s — %s>", category, reason)
}
```

### Staging Content Construction

```
Single left-to-right pass over source file bytes:
1. Sort findings by byte_offset_start ascending
2. token_index counter = 0 (for medium/low findings only)
3. output := []byte{}
4. cursor := 0
5. for each finding:
     output = append(output, content[cursor:finding.ByteOffsetStart]...)
     if finding.Confidence == "high":
       output = append(output, []byte(SentinelTag(finding.Category))...)
       finding.AutoRedacted = true
     else:
       token_index++
       finding.TokenIndex = token_index
       output = append(output, []byte(SentinelTag(finding.Category))...)
     cursor = finding.ByteOffsetEnd
6. output = append(output, content[cursor:]...)
7. Return output
```

### PR Commit Message

Format: `chore(sync): sentinel resolved <category> in <filepath.Base(filename)>:<line>`

Example: `chore(sync): sentinel resolved SECRET in main.go:42`

### Branch Name Generation

```go
func BranchName(prType, repo, description string) string {
    ts := time.Now().Unix()
    slug := slugify(description, 40) // lowercase, hyphens, max 40 chars
    return fmt.Sprintf("sentinel/%s/%s/%s-%d", prType, repo, slug, ts)
}

// slugify: lowercase, replace non-alnum with hyphen, collapse multiple hyphens,
// trim leading/trailing hyphens, truncate to maxLen
```

### PR Title Templates by Type

```
fix:           "fix: <title>"
feat:          "feat: <title>"
docs:          "docs: <title>"
chore:         "chore: <title>"
vulnerability: "fix(security): <title>"
dependency:    "chore(deps): bump <dep> to <version>"
sdlc:          "chore(sdlc): housekeeping for PR #<N>"
```

### PR Body Template Structure

```
## Summary

{{.LLMSummary}}

## Affected Files
{{range .AffectedFiles}}- `{{.}}`
{{end}}

## Changes Made
{{.ChangesDetail}}

---
*Opened by Sentinel · Branch: `{{.Branch}}` → `{{.BaseBranch}}`*
*Task ID: `{{.TaskID}}`*
```

Housekeeping PRs additionally include:
```
## Suggested Workflow
1. Review these housekeeping changes
2. Merge at your convenience — the original PR #{{.RelatedPRN}} has been reviewed
3. To cherry-pick: `git cherry-pick {{.CommitSHA}}`
```

### Housekeeping Developer Comment

Posted to developer's original PR after housekeeping PR is opened:
```
🔧 **Sentinel opened a housekeeping companion PR**

PR #{{.HousekeepingPRNumber}}: [{{.HousekeepingTitle}}]({{.HousekeepingURL}})

This PR includes documentation and changelog updates identified during review.
It targets `{{.BaseBranch}}` and can be merged independently.

React ✅/❌ on the Discord notification or merge/close directly in Forgejo.
```

### Priority Tier Assignment

```go
func PriorityTier(prType string, highTypes []string) PRPriorityTier {
    for _, t := range highTypes {
        if prType == t {
            return PRTierHigh
        }
    }
    return PRTierLow
}
```

Config `pr.high_priority_types` defaults: `["code", "fix", "feat", "vulnerability"]`.

### Discord Embed Construction

```go
func BuildPREmbed(pr SentinelPR, summary string, cfg PRConfig) *discordgo.MessageEmbed {
    var color int
    var prefix string
    if pr.PriorityTier == PRTierHigh {
        color = 0xE74C3C
        if pr.PRType == "vulnerability" {
            prefix = "🚨"
        } else {
            prefix = "🔀"
        }
    } else {
        color = 0x95A5A6
        prefix = "📝"
    }

    title := fmt.Sprintf("%s  %s — %s", prefix, pr.Title, pr.Repo)
    branchField := fmt.Sprintf("`%s` → `%s`", pr.Branch, pr.BaseBranch)

    return &discordgo.MessageEmbed{
        Title:       title,
        Color:       color,
        Description: fmt.Sprintf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n%s", summary),
        Fields: []*discordgo.MessageEmbedField{
            {Name: "Branch", Value: branchField, Inline: false},
            {Name: "Type", Value: pr.PRType, Inline: true},
            {Name: "Forgejo", Value: pr.PRUrl, Inline: false},
        },
        Footer: &discordgo.MessageEmbedFooter{
            Text: "React:  ✅ Merge   ❌ Close   💬 Discuss",
        },
        Timestamp: pr.OpenedAt.Format(time.RFC3339),
    }
}
```

**@here mention cooldown**: Query `sentinel_actions` for most recent `discord_mention_here` for the given repo within `mention_cooldown_minutes`. If found: suppress. Log mention to `sentinel_actions` after posting. Cooldown is per-repo.

### Forgejo↔Discord Sync on Webhook Merge/Close

```go
func (s *SyncHandler) HandlePRWebhook(ctx context.Context, event ForgejoEvent) {
    payload := parsePRPayload(event.Payload)

    // Only handle sentinel-owned PRs
    if !strings.HasPrefix(payload.PullRequest.Head.Ref, "sentinel/") {
        handleDeveloperPRClosed(ctx, payload)
        return
    }

    pr, err := prStore.GetByPRNumber(ctx, payload.Repo, payload.PullRequest.Number)
    if err != nil || pr == nil { return }

    // Idempotency check
    if pr.Status != PRStatusOpen {
        log.Info("PR already in terminal state", "pr", pr.ID, "status", pr.Status)
        return
    }

    merged := payload.Action == "closed" && payload.PullRequest.Merged

    if merged {
        prStore.MarkMerged(ctx, pr.ID, "forgejo-ui")
        discord.EditPRFooter(ctx, pr.DiscordChannelID, pr.DiscordMessageID,
            fmt.Sprintf("✅ Merged in Forgejo · %s", time.Now().Format("15:04 MST")))
        discord.PostChannelMessage(ctx, pr.DiscordChannelID,
            fmt.Sprintf("ℹ️  PR #%d in %s was merged directly in Forgejo · %s",
                pr.PRNumber, pr.Repo, time.Now().Format("15:04 MST")))
    } else {
        prStore.MarkClosed(ctx, pr.ID, "forgejo-ui")
        discord.EditPRFooter(ctx, pr.DiscordChannelID, pr.DiscordMessageID,
            fmt.Sprintf("❌ Closed in Forgejo · %s", time.Now().Format("15:04 MST")))
        discord.PostChannelMessage(ctx, pr.DiscordChannelID,
            fmt.Sprintf("ℹ️  PR #%d in %s was closed directly in Forgejo · %s",
                pr.PRNumber, pr.Repo, time.Now().Format("15:04 MST")))
    }

    actions.Log(ctx, "forgejo_sync_resolved", pr.Repo, pr.ID, ...)
}
```

### ForgejoWorktreeLock Implementation

```go
type forgejoWorktreeLock struct {
    mu    sync.Mutex
    locks map[string]*sync.RWMutex
}

func (f *forgejoWorktreeLock) get(repo string) *sync.RWMutex {
    f.mu.Lock()
    defer f.mu.Unlock()
    if _, ok := f.locks[repo]; !ok {
        f.locks[repo] = &sync.RWMutex{}
    }
    return f.locks[repo]
}

func (f *forgejoWorktreeLock) Lock(repo string)    { f.get(repo).Lock() }
func (f *forgejoWorktreeLock) Unlock(repo string)  { f.get(repo).Unlock() }
func (f *forgejoWorktreeLock) RLock(repo string)   { f.get(repo).RLock() }
func (f *forgejoWorktreeLock) RUnlock(repo string) { f.get(repo).RUnlock() }
```

### SQLite WAL Mode and DB Open

```go
func Open(dsn string) (*sql.DB, error) {
    db, err := sql.Open("sqlite", dsn)
    if err != nil { return nil, err }

    pragmas := []string{
        "PRAGMA journal_mode=WAL",
        "PRAGMA synchronous=NORMAL",
        "PRAGMA busy_timeout=5000",
        "PRAGMA foreign_keys=ON",
    }
    for _, p := range pragmas {
        if _, err := db.Exec(p); err != nil {
            return nil, fmt.Errorf("pragma %q: %w", p, err)
        }
    }

    db.SetMaxOpenConns(1)  // Single writer; Go's sql pool manages connections
    return db, nil
}
```

### skip_if_active_dev_within_hours Check

```go
func HasActiveDevCommits(ctx context.Context, repo string, withinHours int,
    worktreePath string) (bool, error) {
    since := time.Now().Add(-time.Duration(withinHours) * time.Hour)

    r, err := git.PlainOpen(worktreePath)
    if err != nil { return false, err }

    iter, err := r.Log(&git.LogOptions{Since: &since})
    if err != nil { return false, err }

    return iter.ForEach(func(c *object.Commit) error {
        if c.Author.Email == "sentinel@andusystems.com" {
            return nil
        }
        return storer.ErrStop // Non-sentinel commit found → active dev
    }) == storer.ErrStop, nil
}
```

---

## 10. [AI_ASSISTANT] Code Task Spec Template

```go
const taskSpecTemplate = `## Task: {{.ID}}

You are working in repo: {{.Repo}}
Branch to work on: {{.BranchName}}
Base branch: {{.BaseBranch}}

## Description
{{.Description}}

## Affected Files
{{range .AffectedFiles}}- {{.}}
{{end}}

## Acceptance Criteria
{{range .AcceptanceCriteria}}- {{.}}
{{end}}

## Scope Boundary
- Only change files listed in "Affected Files" above.
- Do NOT modify {{.BaseBranch}} directly.
- Do NOT commit to any existing branch other than {{.BranchName}}.
- Do NOT open a pull request — sentinel will open it after your push.
- Do NOT merge any branch.
- Do NOT call external APIs or services.
- Do NOT read files outside this repository's worktree.

## PR Instructions
- Title (for reference only — sentinel will set this): {{.PRTitle}}
- Commit your changes to branch {{.BranchName}}
- Push the branch to origin
- Sentinel detects your push via webhook and opens the PR

## Git Author Identity
Configure git to use:
  Name:  Sentinel
  Email: sentinel@andusystems.com
`
```

The template is rendered via `text/template` and piped to [AI_ASSISTANT] Code CLI via stdin (`cmd.Stdin = strings.NewReader(rendered)`).

---

## 11. PR Review Webhook Flow (Mode 2)

```
1.  [HTTP goroutine] Receive POST /webhooks/forgejo
2.  [HTTP goroutine] Read body (max 10 MB limit)
3.  [HTTP goroutine] Validate HMAC-SHA256 synchronously → HTTP 403 on fail
4.  [HTTP goroutine] Parse X-Gitea-Event header and repo name from payload
5.  [HTTP goroutine] Construct ForgejoEvent and call WebhookQueue.Enqueue()
    → If queue full: return HTTP 429 (Forgejo will retry)
6.  [HTTP goroutine] Return HTTP 200 immediately  ← ACK boundary

    ──── async boundary ────

7.  [Worker goroutine] Dequeue ForgejoEvent
8.  [Worker goroutine] Parse payload fully: action, PR number, head.ref, base.ref,
    merged flag, issue comment body
9.  [Worker goroutine] Determine trigger type:
    - pull_request + action=opened/synchronized → PR review trigger (Branch A)
    - issue_comment containing "/review" → re-review trigger (Branch A)
    - pull_request + action=closed + merged=true → developer PR merged (Branch C)
    - pull_request + action=closed + merged=false → developer PR closed (Branch D)
    - push + head.ref starts with "sentinel/" → [AI_ASSISTANT] Code push detected (Branch B)

    ── BRANCH A: PR review trigger ──

10. [Worker] Acquire per-repo review mutex
11. [Worker] Check pr_dedup: if reviewed within cooldown_minutes → skip, release mutex
12. [Worker] ForgejoWorktreeLock.RLock(repo)
13. [Worker] ForgejoProvider.GetPRDiff(repo, prNumber) → diff string
14. [Worker] ForgejoWorktreeLock.RUnlock(repo)
15. [Worker] LLMClient.ReviewPR(ctx, ReviewOpts{...}) → ReviewResult JSON
    (multi-call if diff too large; merge results)
16. [Worker] Build review comment body (verdict + per-file notes + assessments)
17. [Worker] ForgejoProvider.PostReview(repo, prNumber, verdict, body)
18. [Worker] Insert into pr_dedup (repo, prNumber, reviewed_at)
19. [Worker] If ReviewResult.HousekeepingFiles non-empty:
    a. ForgejoWorktreeLock.Lock(repo)
    b. PRCreator.CreateBranch("sentinel/sdlc/<repo>/pr<N>-housekeeping-<ts>")
    c. LLMClient.WriteHousekeepingBody(HousekeepingOpts{...}) → prose
    d. PRCreator.CommitAndPush(repo, branch, "chore(sdlc): housekeeping for PR #N", files)
    e. ForgejoWorktreeLock.Unlock(repo)
    f. PRCreator.OpenPR(OpenPROptions{BaseBranch: developer's base branch, Tier: low})
    g. PRNotifier.PostPRNotification(ctx, housekeepingPR, prose) → messageID
    h. SentinelPRStore.Create(housekeepingPR with RelatedPRNumber=prNumber)
    i. ForgejoProvider.PostPRComment(repo, prNumber, housekeepingDeveloperComment)
20. [Worker] If ReviewResult.CodeFixNeeded:
    a. ForgejoWorktreeLock.Lock(repo)
    b. PRCreator.CreateBranch("sentinel/fix/<repo>/<slug>-<ts>")
    c. Build TaskSpec from ReviewResult.CodeFixDescription
    d. Insert task record to DB (status=pending)
    e. TaskExecutor.Execute(ctx, spec, branch, repo)
       (acquires global [AI_ASSISTANT] Code semaphore; write lock held until Execute returns)
    f. ForgejoWorktreeLock.Unlock(repo)
    g. [Later, via push webhook Branch B] Sentinel opens PR + Discord notification
21. [Worker] For each IssueSpec in ReviewResult.IssueSpecs:
    body ← LLMClient.WriteProse(ProseOpts{Role: "issue", Context: spec})
    ForgejoProvider.CreateIssue(repo, IssueOptions{Title, Body, Labels})
22. [Worker] store.Actions.Log("mode2_review_complete", ...)
23. [Worker] Release per-repo review mutex

    ── BRANCH B: [AI_ASSISTANT] Code push detected ──

24. [Worker] Parse branch name from push payload
25. [Worker] Lookup task record by branch name in DB
26. [Worker] Build PR title from task; ForgejoProvider.CreatePR
27. [Worker] PRNotifier.PostPRNotification (high-priority) → messageID
28. [Worker] SentinelPRStore.Create
29. [Worker] Update task record status=pr_opened

    ── BRANCH C: Developer PR merged ──

30. [Worker] Check for open SentinelPR with RelatedPRNumber = this PR number
31. [Worker] If found: ForgejoProvider.PostPRComment(housekeepingPRNumber,
    "Original PR #N was merged. You can now merge this housekeeping PR at your convenience.")
32. [Worker] Surface in nightly digest (mark in DB)

    ── BRANCH D: Developer PR closed without merge ──

33. [Worker] Check for open SentinelPR with RelatedPRNumber = this PR number
34. [Worker] If found:
    ForgejoProvider.PostPRComment(housekeepingPRNumber, "Original PR #N was closed...")
    ForgejoProvider.ClosePR(housekeepingPRNumber)
    SentinelPRStore.MarkClosed(id, "sentinel-auto-close")
    Discord.EditPRFooter(..., "❌ Auto-closed — original PR closed without merge")
```

---

## 12. Forgejo → GitHub Sync Flow (Mode 3)

```
1.  SyncRunner.Sync(ctx, repo) called (by nightly pipeline or manual trigger)
2.  Create SyncRun record: status="running", mode=3
3.  ForgejoWorktreeLock.Lock(repo)
4.  go-git pull (fetch + merge) on Forgejo worktree
5.  Current HEAD SHA = forgejo_sha
6.  ForgejoWorktreeLock.Unlock(repo)

7.  ForgejoWorktreeLock.RLock(repo)
8.  Load last synced SHA from repo_sync_state (or empty tree SHA if first run)
9.  go-git diff between last_sha and forgejo_sha → list of changed files
10. ForgejoWorktreeLock.RUnlock(repo)

11. For each changed file (sequential):
    a. Check for existing pending_resolutions for (repo, filename) with status='pending'
       → If any: skip re-sanitization; copy current content to GitHub staging as-is
    b. ApprovedValuesStore.GetSkipZones(ctx, repo, fileContent)
    c. SanitizationPipeline.SanitizeFile(ctx, SanitizeFileOpts{...})
    d. WorktreeManager.WriteGitHubStaging(ctx, repo, filename, sanitizedContent)
    e. For each high-confidence finding: stored in sanitization_findings (AutoRedacted=true)
    f. For each medium/low finding:
       - PendingResolutionStore.Create(ctx, resolution)
       - DiscordBot.PostFinding(ctx, resolution, finding) → messageID
       - DiscordBot.SeedFindingReactions(ctx, channelID, messageID)
       - ForgejoProvider.CreateIssue(ctx, repo, IssueOptions{...})
       - Update pending_resolution with forgejo_issue_number
    g. Increment SyncRun counters

12. WorktreeManager.PushAllStaging(ctx, repo,
       "chore(sync): sentinel initial sync from Forgejo HEAD")

13. Update repo_sync_state with new forgejo_sha
14. SyncRun.status = "complete" or "complete_with_pending"
15. SyncRun.completed_at = now()
16. DiscordBot.PostChannelMessage(ctx, findingsChannelID,
       "✅ Sync complete for <repo>: N files synced, M pending findings")
17. store.Actions.Log("mode3_sync_complete", ...)
```

---

## 13. Initial Migration Flow (Mode 4)

```
1.  Operator runs: sentinel migrate --repo <name> [--force]
    OR Discord command: /sentinel migrate <name>

2.  Pre-checks:
    a. Verify repo exists in config and is not excluded
    b. Check migration_status: if status="complete" and not --force → abort with error
    c. ForgejoWorktreeLock.Lock(repo); git pull; Unlock(repo)
    d. GitHub API: check if mirror repo exists and is non-empty
       - Non-empty + no --force → abort, Discord alert, return error
       - Non-empty + --force:
           i.  Post confirmation embed to command channel
           ii. PendingConfirmationsStore.Create(TTL=10 minutes)
           iii. Block (channel wait) for operator ✅ reaction (max 10 min)
           iv.  If TTL expires: post "Confirmation expired — re-run to try again"
                Update status="expired"; return
           v.   If ❌: post "Migration cancelled"; update status="rejected"; return
           vi.  On ✅: proceed

3.  Update migration_status: status="in_progress", started_at=now()

4.  ForgejoWorktreeLock.Lock(repo)
5.  Walk all files in Forgejo worktree HEAD snapshot (go-git tree walk)
6.  ForgejoWorktreeLock.Unlock(repo)

7.  For each file (sequential):
    a. Read file content
    b. Check skip_patterns
    c. ApprovedValuesStore.GetSkipZones(ctx, repo, content)
    d. SanitizationPipeline.SanitizeFile(ctx, SanitizeFileOpts{...})
    e. WorktreeManager.WriteGitHubStaging(ctx, repo, filename, sanitizedContent)
    f. High-confidence findings: auto-redacted, stored, no pending
    g. Medium/low findings:
       - PendingResolutionStore.Create
       - DiscordBot.PostFinding + SeedFindingReactions
       - ForgejoProvider.CreateIssue
    h. Increment counters

8.  WorktreeManager.PushAllStaging(ctx, repo,
       "chore(migrate): initial sentinel migration of <repo>")
    (single squashed commit; GitHub is mirror only)

9.  Update repo_sync_state: forgejo_sha = HEAD SHA (Mode 3 baseline)
10. Update migration_status: status="complete", completed_at=now()

11. DiscordBot.PostChannelMessage(ctx, commandChannelID,
       "✅ Migration complete: <repo>\n"+
       "  Files migrated: N\n"+
       "  High-confidence auto-redacted: M\n"+
       "  Pending operator review: P\n"+
       "  Forgejo HEAD: <sha>")

12. store.Actions.Log("mode4_migration_complete", ...)
```

---

## 14. Concurrency Model

### Goroutines and Primitives

| Component | Goroutine model | Synchronization |
|---|---|---|
| Nightly pipeline | Sequential repos, sequential tasks per repo | Per-repo Forgejo write lock |
| HTTP webhook server | 1 goroutine per connection (net/http default) | None (stateless ACK) |
| Webhook event queue | Buffered channel (`chan ForgejoEvent`, size 100) | Channel send/receive |
| Webhook workers | N goroutines (default 4) reading from queue | Per-repo processing mutex |
| [AI_ASSISTANT] Code executor | 1 goroutine per invocation | `chan struct{}` semaphore (size 1) |
| Ollama client | 1 goroutine per call | `chan struct{}` semaphore (size 1) |
| [AI_ASSISTANT] API client | N goroutines, rate-limited | `golang.org/x/time/rate` token bucket |
| ForgejoWorktreeLock | Per-repo `sync.RWMutex` in map | Global `sync.Mutex` for map access |
| FileMutexRegistry | Per-(repo,filename) `sync.Mutex` in map | Global `sync.Mutex` for map access |
| PR reaction handlers | One goroutine per reaction event | Per-PR `sync.Mutex` (first-reaction-wins) |
| Finding reaction handlers | One goroutine per reaction event | FileMutexRegistry lock |
| Discord thread Q&A | Buffered channel → Ollama semaphore | Ollama semaphore |
| Cron scheduler | robfig/cron internal goroutine | No shared state beyond DB |

### Per-Repo Processing Mutex

Webhook processor holds a per-repo mutex during Mode 2 review to prevent concurrent reviews for the same repo. Separate from `ForgejoWorktreeLock`.

### Graceful Shutdown

```
1. Receive SIGTERM/SIGINT
2. cron.Stop() — wait for in-flight nightly job (timeout 5 min)
3. webhook.Server.Shutdown(ctx with 30s timeout) — stop accepting connections
4. Close WebhookQueue channel — no new enqueues
5. Wait for all N webhook workers to drain (timeout 2 min)
6. discord.Stop() — close gateway cleanly
7. db.Close() — WAL checkpoint flushed on close
8. Verify no orphaned worktree lock holders
9. os.Exit(0)
```

All goroutines accept a `context.Context` derived from the root context. On shutdown, root context is cancelled.

---

## 15. Helm Chart Specification

### `charts/sentinel/Chart.yaml`
```yaml
apiVersion: v2
name: sentinel
description: Autonomous SDLC orchestration engine
type: application
version: 0.1.0
appVersion: "0.1.0"
```

### `charts/sentinel/values.yaml`
```yaml
image:
  repository: registry.andusystems.com/sentinel
  tag: latest
  pullPolicy: IfNotPresent

replicaCount: 1  # MUST be 1 — SQLite requires exclusive PVC access

config:
  forgejo:
    baseUrl: "https://git.andusystems.com"
  webhook:
    port: 8080
  ollama:
    host: "http://ollama.sentinel.svc.cluster.local:11434"
  worktree:
    basePath: "/data/workspace"
  ingressHost: "sentinel.andusystems.com"

secrets:
  forgejoSentinelToken: ""
  forgejoOperatorToken: ""
  discordBotToken: ""
  anthropicApiKey: ""
  githubToken: ""
  forgejoWebhookSecret: ""

persistence:
  forgejo:
    storageClass: "longhorn"
    accessModes: ["ReadWriteOnce"]   # RWO — never RWX
    size: "20Gi"
  github:
    storageClass: "longhorn"
    accessModes: ["ReadWriteOnce"]   # RWO — stores full repo HEAD snapshots
    size: "50Gi"
  db:
    storageClass: "longhorn"
    accessModes: ["ReadWriteOnce"]   # RWO — SQLite exclusive file access required
    size: "5Gi"

ingress:
  tlsSecret: "sentinel-tls"
  certIssuer: "letsencrypt-prod"

resources:
  requests:
    memory: "256Mi"
    cpu: "100m"
  limits:
    memory: "1Gi"
    cpu: "500m"
```

### `templates/deployment.yaml` (key spec)

- `replicas: {{ .Values.replicaCount }}` — always 1
- All six env vars from `sentinel-secrets` Secret
- Three PVC volume mounts: `/data/db`, `/data/workspace/forgejo`, `/data/workspace/github`
- ConfigMap mount at `/etc/sentinel`
- Liveness probe: `GET /health` on webhook port, `initialDelaySeconds: 10`
- Readiness probe: `GET /ready` on webhook port, `initialDelaySeconds: 5`

### `templates/pvc-forgejo.yaml`

```yaml
# IMPORTANT: accessModes MUST be ReadWriteOnce (RWO).
# Longhorn RWO guarantees only one node mounts this volume.
# Changing to RWX would allow multiple pods to write simultaneously,
# corrupting go-git worktree state and SQLite database.
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: sentinel-workspace-forgejo
  namespace: sentinel
spec:
  accessModes:
    - ReadWriteOnce    # DO NOT change to ReadWriteMany
  storageClassName: longhorn
  resources:
    requests:
      storage: {{ .Values.persistence.forgejo.size }}
```

### `templates/pvc-github.yaml`

```yaml
# IMPORTANT: accessModes MUST be ReadWriteOnce (RWO).
# GitHub staging workspace stores full repo HEAD snapshots.
# RWO prevents concurrent node access that would corrupt worktree state.
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: sentinel-workspace-github
  namespace: sentinel
spec:
  accessModes:
    - ReadWriteOnce    # DO NOT change to ReadWriteMany
  storageClassName: longhorn
  resources:
    requests:
      storage: {{ .Values.persistence.github.size }}
```

### `templates/ingressroute.yaml`

```yaml
apiVersion: traefik.io/v1alpha1
kind: IngressRoute
metadata:
  name: sentinel-webhook
  namespace: sentinel
spec:
  entryPoints:
    - websecure
  routes:
    - match: Host(`{{ .Values.config.ingressHost }}`) && PathPrefix(`/webhooks`)
      kind: Rule
      services:
        - name: sentinel
          port: {{ .Values.config.webhook.port }}
  tls:
    secretName: {{ .Values.ingress.tlsSecret }}
```

### `templates/certificate.yaml`

```yaml
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: sentinel-tls
  namespace: sentinel
spec:
  secretName: sentinel-tls
  issuerRef:
    name: {{ .Values.ingress.certIssuer }}
    kind: ClusterIssuer
  dnsNames:
    - {{ .Values.config.ingressHost }}
```

### `templates/networkpolicy.yaml`

Egress allowlist — only to:
- Forgejo instance IP port 443
- `api.github.com` port 443
- Ollama internal cluster DNS port 11434
- `api.[AI_PROVIDER].com` port 443
- `discord.com` / `gateway.discord.gg` port 443
- DNS (UDP 53)

---

## 16. ArgoCD Application Manifest

```yaml
# argocd/sentinel-app.yaml
# IMPORTANT: syncPolicy is MANUAL (not automated).
# Sentinel has side effects on Forgejo, GitHub, and Discord.
# An automated bad deploy could open spurious PRs or send erroneous Discord messages.
# Always review changes before syncing in ArgoCD UI.
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: sentinel
  namespace: argocd
  annotations:
    argocd.argoproj.io/sync-wave: "10"
spec:
  project: default
  source:
    repoURL: https://git.andusystems.com/andusystems/sentinel
    targetRevision: HEAD
    path: charts/sentinel
    helm:
      valueFiles:
        - values.yaml
        - values-prod.yaml
  destination:
    server: https://kubernetes.default.svc
    namespace: sentinel
  syncPolicy:
    # NO automated sync — operator must approve each deploy in ArgoCD UI
    syncOptions:
      - CreateNamespace=true
      - PrunePropagationPolicy=foreground
  ignoreDifferences:
    - group: ""
      kind: PersistentVolumeClaim
      jsonPointers:
        - /status
    - group: apps
      kind: Deployment
      jsonPointers:
        - /metadata/annotations/deployment.kubernetes.io~1revision
    - group: cert-manager.io
      kind: Certificate
      jsonPointers:
        - /status
```

---

## 17. Makefile

```makefile
.PHONY: build run dry-run webhook-test sync-dry-run llm-test migrate \
        migrate-dry-run sanitize-test [AI_ASSISTANT]-api-test discord-test \
        reaction-test pr-reaction-test token-index-test forgejo-sync-test \
        install test lint helm-lint docker-build docker-push

# Compile sentinel binary to ./bin/sentinel
build:
	go build -o bin/sentinel ./cmd/sentinel

# Run sentinel with full schedule (nightly cron + webhook server + Discord bot)
run:
	./bin/sentinel --config config.yaml

# Run analysis only: no PRs, no GitHub pushes; output findings to Discord
dry-run:
	./bin/sentinel --config config.yaml --dry-run --repo $(REPO)

# Send synthetic Forgejo pull_request and push webhook payloads to local server
webhook-test:
	go run ./tools/webhook-test --url http://localhost:8080/webhooks/forgejo \
		--secret $(FORGEJO_WEBHOOK_SECRET) \
		--fixture fixtures/webhook_pr_open.json

# Run Mode 3 sanitization and log findings; do not push to GitHub
sync-dry-run:
	./bin/sentinel --config config.yaml --mode sync --repo $(REPO) --dry-run

# Send fixture diff to Ollama and print returned TaskSpec JSON
llm-test:
	go run ./tools/llm-test --fixture fixtures/diff_small.patch --config config.yaml

# Trigger Mode 4 migration for a repo
migrate:
	./bin/sentinel --config config.yaml --mode migrate --repo $(REPO)

# Run full sanitization pass on all repo files; print findings report; no GitHub push
migrate-dry-run:
	./bin/sentinel --config config.yaml --mode migrate --repo $(REPO) --dry-run

# Run all three sanitization layers on fixture files; print findings
sanitize-test:
	go run ./tools/sanitize-test --fixture fixtures/secret_file.go --config config.yaml

# Send fixture file chunk to [AI_ASSISTANT] API sanitization endpoint; print findings
[AI_ASSISTANT]-api-test:
	go run ./tools/[AI_ASSISTANT]-api-test --fixture fixtures/secret_file.go --config config.yaml

# Connect Discord bot; post synthetic finding + PR notification; verify reactions fire
discord-test:
	go run ./tools/discord-test --config config.yaml

# Simulate all four finding reactions (✅/❌/🔍/✏️) against a test finding in DB
reaction-test:
	go run ./tools/reaction-test --config config.yaml --type finding

# Simulate all PR reactions (✅/❌/💬) and Forgejo webhook merge/close; verify sync
pr-reaction-test:
	go run ./tools/reaction-test --config config.yaml --type pr

# Run unit tests for token_index algorithm only (fastest correctness check)
token-index-test:
	go test ./internal/worktree/ -run TestTokenIndex -v

# Simulate Forgejo webhook merge/close events; verify Discord embed + channel message
forgejo-sync-test:
	go run ./tools/forgejo-sync-test --config config.yaml

# Copy binary to /usr/local/bin and run Helm install in sentinel namespace
install:
	cp bin/sentinel /usr/local/bin/sentinel
	helm install sentinel charts/sentinel -n sentinel --create-namespace -f values-local.yaml

# Run all Go tests
test:
	go test ./... -race -timeout 5m

# Run golangci-lint
lint:
	golangci-lint run ./...

# Lint Helm charts
helm-lint:
	helm lint charts/sentinel

# Build Docker image
docker-build:
	docker build -t registry.andusystems.com/sentinel:$(shell git rev-parse --short HEAD) .

# Push Docker image to registry
docker-push:
	docker push registry.andusystems.com/sentinel:$(shell git rev-parse --short HEAD)
```

---

## 18. Testing Strategy

### Unit Tests

**Config parsing** (`internal/config/validate_test.go`):
- All fields parsed correctly from YAML
- Env var references resolved
- `category_reasons` value containing `>` → validation error at load time
- Missing required fields → descriptive error
- Per-repo overrides override global defaults

**token_index algorithm** (`internal/worktree/token_index_test.go`) — highest priority:
- 1 finding: resolve replaces correct occurrence
- 3 findings: resolve in order 1→2→3, each replaces correct tag
- 3 findings: resolve in reverse order 3→2→1, `resolvedCount` computed correctly
- 5 findings: skip index 2 (rejected), resolve 1, 3, 4, 5 — correct positions
- `resolvedCount` = 0 when no predecessors resolved
- `resolvedCount` = 2 when 2 predecessors with lower tokenIndex are resolved
- Error: file has fewer tags than expected tokenIndex → error returned, no write
- Error: `targetPos` out of range → error returned, no write
- Multi-line value: sentinel tag is single-line with count note

**Sentinel tag construction** (`internal/sanitize/tag_test.go`):
- All 6 categories produce correct tag string
- Multi-line value collapsed to single line with `(N lines collapsed)` note

**Staging content construction** (`internal/sanitize/staging_test.go`):
- Findings sorted by byte_offset_start before pass
- High-confidence findings: auto-redacted, no token_index assigned
- Medium/low findings: token_index assigned sequentially (1, 2, 3...)
- Mixed confidence: correct interleaving
- No findings: content unchanged

**Commit message format** (`internal/worktree/push_test.go`):
- `resolutionCommitMsg("SECRET", "internal/config/config.go", 42)` → expected format

**HMAC webhook validation** (`internal/webhook/hmac_test.go`):
- Valid HMAC → true; invalid → false; empty secret → false
- Uses `hmac.Equal` (constant-time)

**Async webhook ACK** (`internal/webhook/handler_test.go`):
- HTTP 200 returned before processing goroutine completes

**Webhook queue** (`internal/webhook/queue_test.go`):
- Enqueue/dequeue N events in order
- Full queue → Enqueue returns error; handler returns HTTP 429
- Dequeue returns after context cancelled

**ForgejoWorktreeLock** (`internal/worktree/lock_test.go`):
- 10 concurrent write-lock requests: only 1 holds at a time
- Multiple read locks held simultaneously: no deadlock
- Write lock blocks until all read locks released

**Finding reaction handlers** (`internal/discord/reactions_test.go`):
- ✅ approve: happy path, wrong user, already-approved idempotency
- ❌ reject: happy path
- 🔍 re-analyze: reanalyzing status set, [AI_ASSISTANT] API called, resolution superseded
- ✏️ discuss: thread opened, LLM answer posted
- First-reaction-wins: concurrent ✅ and ❌ (per-file mutex)

**PR reaction handlers** (`internal/prnotify/reactions_test.go`):
- ✅: MergePR called with operator token; wrong user → no merge; already-merged → idempotent
- ❌: ClosePR called with sentinel token
- 💬: thread opened, LLM connected
- Concurrent ✅ and ❌: one wins (per-PR mutex)

**Forgejo webhook → Discord sync** (`internal/prnotify/sync_test.go`):
- Merge event: MarkMerged + EditPRFooter + PostChannelMessage
- Close event: MarkClosed + EditPRFooter + PostChannelMessage
- Duplicate merge event: idempotent no-op
- Non-sentinel PR merge: housekeeping check runs, not sync

**PR priority tier assignment** (`internal/pipeline/router_test.go`):
- All type values produce correct tier

**Discord embed construction** (`internal/discord/embed_test.go`):
- High: color=0xE74C3C, prefix=🔀; vulnerability: prefix=🚨; low: color=0x95A5A6, prefix=📝

**Multi-call LLM batching** (`internal/llm/batcher_test.go`):
- Token budget respected per batch
- Focus-area files in earlier batches
- Results merged, deduplicated, re-ranked; max_tasks_per_run cap applied

**approved_values skip zones** (`internal/sanitize/skipzones_test.go`):
- Skip zones computed correctly; sanitization skips findings within zones

### Integration Tests (with Mocks)

All external interfaces mocked via interface substitution. No real network calls.

1. Full Mode 1 nightly: analysis → 3 branches → 3 PRs → 3 Discord notifications at correct tiers
2. Full Mode 2: webhook → review comment + housekeeping PR + [AI_ASSISTANT] Code fix PR
3. Discord ✅ → Forgejo merge → DB update → embed edit
4. Forgejo merge webhook → embed update + channel message
5. Forgejo close webhook → embed update + channel message
6. Idempotent: Discord ✅ after Forgejo-merged PR → no duplicate merge
7. Full Mode 3 sync: high finding auto-redacted, medium pending, clean pushed
8. Full Mode 4 migration with --force: TTL confirmation → migration → squashed push → pending findings
9. Multi-finding same file: resolve in random order → all correct via token_index
10. Developer merges own PR → housekeeping PR comment posted
11. Developer closes own PR → housekeeping PR auto-closed

### Manual Validation Only

- Ollama output quality (Roles A–G — subjective evaluation)
- [AI_ASSISTANT] API finding quality vs Layer 2 comparison
- [AI_ASSISTANT] Code PR quality on a real repo
- Discord embed visual appearance (colors, emoji, link rendering)
- ArgoCD UI sync behavior and diff presentation
- Longhorn volume attachment on node restart

---

## 19. Security Considerations

1. **Token separation and least privilege**: `FORGEJO_SENTINEL_TOKEN` has no merge permission at Forgejo account level (enforced by Forgejo role, not code alone). `FORGEJO_OPERATOR_TOKEN` used in exactly one code path. Document rotation in operator runbook.

2. **Webhook HMAC validation**: HMAC-SHA256 via `hmac.Equal` (constant-time) on every webhook before any parsing. HTTP 403 on fail with source IP logged. Secret never logged. Rotation requires updating both Forgejo webhook setting and Kubernetes Secret.

3. **`original_value` never logged**: stored in DB but never written to log output. Accessed only inside `FileMutexRegistry.Lock` during approve/reject paths.

4. **Discord operator allowlist**: only user IDs in `config.discord.operator_user_ids` can trigger merge/close. Bot accounts explicitly excluded.

5. **[AI_ASSISTANT] Code scope boundary**: task spec template instructs no commits to existing branches, no PR creation, no external API calls. Forgejo branch protection on `main` is the enforcement backstop. NetworkPolicy limits egress.

6. **No direct Forgejo branch writes**: sentinel never pushes to `main` or any developer branch. All changes go to `sentinel/`-prefixed branches only. No code path constructs a push to an existing Forgejo branch.

7. **Webhook HMAC before enqueue**: validation is synchronous before `Enqueue`. Malicious payloads cannot poison the queue.

8. **MergePR failure handling**: if operator token is expired, `ForgejoProvider.MergePR` returns error → log + Discord alert + do NOT call `MarkMerged`. Operator retries or merges in Forgejo UI.

9. **Branch protection on Forgejo**: all watched repos should have branch protection on primary branches. Documented in operator runbook as required setup.

10. **`category_reasons` injection prevention**: config validation rejects any reason string containing `>` at load time, preventing malformed sentinel tags.

11. **Custom token validation**: `✏️` path validates replacement value against `^<[A-Z][A-Z0-9_]{0,98}>$` before any file write.

12. **Longhorn RWO PVCs**: SQLite corruption prevention. Helm chart `accessModes: [ReadWriteOnce]` with comment: "DO NOT change to ReadWriteMany — SQLite requires exclusive file access."

13. **approved_values**: Discord confirmation + 10-minute TTL + 256-char limit + full audit in `sentinel_actions`.

14. **Secrets in logs**: slog custom handler masks field values matching configured token env var names. No API token ever appears in a log line.

---

## 20. Open Questions & Decisions

**1. gitleaks: library vs subprocess**

**Decision: library**. `github.com/gitleaks/gitleaks/v8` exposes `detect.NewDetector` and `detector.DetectReader` as public API without subprocess. Wrap behind `GitleaksAdapter` interface in `internal/sanitize/layer1_gitleaks.go`. If future major version breaks the API, fall back to `gitleaks detect --report-format json --no-git --pipe` with no calling-code change.

**2. [AI_ASSISTANT] Code PR opening**

**Decision: sentinel opens the PR, not [AI_ASSISTANT] Code**. [AI_ASSISTANT] Code pushes the branch and exits. Sentinel detects the push via the `push` webhook, looks up the task by branch name in `tasks` table, and calls `ForgejoProvider.CreatePR`. All PR metadata remains under sentinel's deterministic Go control.

**3. Housekeeping PR on developer PR merge**

**Decision**: Post comment on housekeeping PR: "Original PR #N was merged. You can merge this housekeeping PR at your convenience." Do NOT auto-merge. Include in next nightly digest under "Pending Housekeeping". Human approval always required.

**4. Housekeeping PR on developer PR close (without merge)**

**Decision**: Sentinel auto-closes the orphaned housekeeping PR. Post comment: "Original PR #N was closed without merging. This housekeeping PR is no longer relevant." Call `ForgejoProvider.ClosePR` with sentinel token. Call `SentinelPRStore.MarkClosed(id, "sentinel-auto-close")`. Edit Discord embed footer accordingly. No operator approval needed.

**5. @here mention cooldown implementation**

**Decision**: Query `sentinel_actions` for most recent `discord_mention_here` for the given repo within `mention_cooldown_minutes`. If found: suppress. Log mention to `sentinel_actions` after posting. Cooldown is per-repo, not global. Default: 60 minutes. Survives process restarts.

**6. Mode 4 --force confirmation TTL**

**Decision**: 10 minutes (configurable via `config.allowlist.confirmation_ttl_minutes`). Stored in `pending_confirmations.expires_at`. On reaction: check expiry first. If expired: post "Confirmation expired — re-run the command to try again", set status=`expired`. 10 minutes is long enough for an operator watching Discord, short enough to prevent unintended late confirmation.

**7. SQLite WAL mode**

**Decision**: Enable WAL unconditionally at DB open time. Apply `PRAGMA journal_mode=WAL; PRAGMA synchronous=NORMAL; PRAGMA busy_timeout=5000; PRAGMA foreign_keys=ON;` in `store/db.go` `Open()` after schema migrations. Use `SetMaxOpenConns(1)` on the `*sql.DB` instance. WAL allows multiple concurrent readers and one writer. `busy_timeout=5000` prevents `SQLITE_BUSY` errors under the 4-worker webhook pool. Auto-checkpoint at 1000 pages is sufficient.

**8. Forgejo webhook registration**

**Decision**: Auto-register at startup, idempotent. For each watched repo: list existing webhooks, check if sentinel's URL exists — if yes update, if no create. Use `FORGEJO_WEBHOOK_SECRET` from env. Events: `pull_request`, `push`, `issue_comment`. URL: `https://<config.ingressHost>/webhooks/forgejo`. Document verification in operator runbook. Token rotation: update Kubernetes Secret + restart pod (auto-updates on startup).
