# sentinel — [AI_ASSISTANT] Code Planning Prompt (v14 — FINAL)
#
# USAGE: Run in an empty directory that will become the sentinel repo.
#   [AI_ASSISTANT] < PLAN_PROMPT_v14.md
# OR paste into an interactive [AI_ASSISTANT] Code session.
# ---------------------------------------------------------------
# After reading this document, produce a complete implementation-ready
# plan. Do NOT write any code. Output only the plan document, then the
# single line: PLAN COMPLETE
# ---------------------------------------------------------------

You are a software architect. Your job is to produce a complete,
implementation-ready plan for `sentinel` — a Go application that acts
as an autonomous SDLC orchestration engine. Do NOT write implementation
code. Produce only the plan document described at the end of this prompt.

---

## Core Design Philosophy: LLM Routing

| Executor | Owns | Never does |
|---|---|---|
| **Go** | Structure, mechanics, API calls, formatting, deterministic transforms | Write prose, analyze code |
| **Local LLM (Ollama/qwen2.5-coder:14b)** | Analysis, triage, prose, review body, doc text, sanitization semantic pass, discussion Q&A | Write or modify source code |
| **[AI_ASSISTANT] API** | Final semantic sanitization pass + re-analysis on operator request | Write code, open PRs, anything local LLM handles |
| **[AI_ASSISTANT] Code CLI** | All source code changes, complex rewrites, security remediations | Sanitization, deterministic tasks, prose |

[AI_ASSISTANT] tokens are spent exclusively where [AI_ASSISTANT]'s quality is
irreplaceable. Everything else is Go or local LLM.

---

## Core Design Philosophy: Non-Blocking Operations

**Nothing in sentinel ever blocks another operation.**

Pending sanitization findings and pending PR approvals do not block
any pipeline, any repo, or any other sentinel function. Work items
are tracked in the DB and surfaced via Discord. Sentinel continues
operating on other tasks while waiting for human input.

---

## Core Design Philosophy: Human Approval Required

**sentinel NEVER commits directly to any branch on Forgejo.**

Every sentinel-authored change is:
1. Committed to a new sentinel-owned branch
2. Opened as a Forgejo pull request
3. Notified via Discord with approval reactions
4. Merged only after explicit operator approval via Discord reaction
   OR directly in the Forgejo UI — both paths are fully supported
   and kept in sync

**No exceptions on Forgejo.** GitHub mirror (Modes 3/4) is the only
place where direct push is acceptable — it is a backup copy only.

---

## Core Design Philosophy: Bidirectional Forgejo↔Discord Sync

**Every state change on a sentinel PR is reflected in both systems.**

| Event | Forgejo action | Discord action |
|---|---|---|
| Sentinel opens PR | PR created | Notification embed posted |
| Operator reacts ✅ in Discord | PR merged via operator token | Embed footer updated |
| Operator reacts ❌ in Discord | PR closed via sentinel token | Embed footer updated |
| Operator merges in Forgejo UI | PR already merged | Embed footer updated + brief channel message |
| Operator closes in Forgejo UI | PR already closed | Embed footer updated + brief channel message |
| New commits pushed to sentinel PR | PR updated | Thread message posted if discussion open |
| Developer merges their own PR | (detected via webhook) | Associated housekeeping PR noted in digest |

This sync is implemented via:
- **Discord → Forgejo**: reaction handlers call Forgejo API
- **Forgejo → Discord**: `pull_request` webhook events update DB and
  Discord embeds. sentinel subscribes to all `pull_request` events
  and inspects `head.ref` to identify sentinel-owned PRs.

---

## Dual-Token Authentication Model

**`FORGEJO_SENTINEL_TOKEN`** — sentinel service account:
- Creates branches, opens PRs, posts comments/reviews, creates issues,
  closes PRs, manages labels/milestones
- NO merge permissions (enforced at Forgejo account level)

**`FORGEJO_OPERATOR_TOKEN`** — operator's personal account PAT:
- Used ONLY for merging PRs when operator reacts ✅ in Discord
- Long-lived PAT stored in a dedicated Kubernetes Secret
- Never used for creating content — only for the merge API call
- Document in operator runbook: rotate this token if compromised

Both tokens in separate Kubernetes Secrets, mounted as separate env vars.

---

## PR Priority Tiers

All sentinel PRs require human approval. Discord notifications are
tiered so security issues aren't buried by docs updates.

**High-priority** (`code`, `fix`, `feat`, `vulnerability`):
- Embed color: red `0xE74C3C`
- Prefix: 🚨 (vulnerability) or 🔀 (other code)
- `@here` mention if type is `vulnerability` (configurable cooldown,
  default 1 hour to prevent spam across a single nightly run)

**Low-priority** (`docs`, `chore`, `dependency`, `sdlc-housekeeping`):
- Embed color: grey `0x95A5A6`
- Prefix: 📝
- No mention/ping

Both tiers use identical ✅/❌/💬 reaction handlers and `SentinelPRStore`.

---

## Forgejo↔Discord State Machine for Sentinel PRs

```
         OPEN
          │
    ┌─────┴──────┐
    │            │
✅ Discord    ✅ Forgejo UI
    │            │
    ▼            ▼
  MERGED ←──────┘
  (embed updated, channel message posted)

    │
    │  ❌ Discord    ❌ Forgejo UI
    └─────┬──────────────┘
          ▼
        CLOSED
  (embed updated, channel message posted)
```

State transitions are idempotent — if a PR is already in a terminal
state (merged/closed), subsequent events for the same PR are logged
but produce no further Discord or Forgejo side effects.

---

## PR Notification Embed Format

**High-priority (code/fix/feat/vulnerability):**
```
🚨  Security Fix — fleetdock
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
Branch:   sentinel/fix/fleetdock/jwt-secret-env-1704067200  →  main
Type:     fix · security
Opened:   <timestamp>

<LLM-written one-paragraph summary>

Forgejo:  <PR URL>

React:  ✅ Merge   ❌ Close   💬 Discuss
```

**Low-priority (docs/chore/dependency/sdlc-housekeeping):**
```
📝  Docs Update — fleetdock
━━━━━━━━━━━━━━━━━━━━━━━━━
Branch:   sentinel/docs/fleetdock/update-readme-arch-1704067200  →  main
Type:     docs
Opened:   <timestamp>

<LLM-written one-paragraph summary>

Forgejo:  <PR URL>

React:  ✅ Merge   ❌ Close   💬 Discuss
```

**On Forgejo-side resolution, sentinel additionally posts a brief
message to the PR channel** (not just editing the embed footer):
```
ℹ️  PR #48 in fleetdock was merged directly in Forgejo · <time>
```
or:
```
ℹ️  PR #48 in fleetdock was closed directly in Forgejo · <time>
```

---

## Branch Naming Convention

```
sentinel/<type>/<repo-slug>/<description-slug>-<unix-ts>
```

Types: `fix`, `feat`, `docs`, `chore`, `sdlc`

Examples:
- `sentinel/fix/fleetdock/jwt-secret-env-1704067200`
- `sentinel/docs/fleetdock/update-readme-1704067200`
- `sentinel/sdlc/fleetdock/pr42-housekeeping-1704067200`
- `sentinel/chore/fleetdock/bump-golang-x-net-1704067200`

Go generates deterministically. No LLM involvement. Max 40 char slug.

---

## Sentinel User

All actions attributed to `sentinel` service account:
- Forgejo user `sentinel` — write access, PR/issue/review, NO merge
- GitHub user/bot `sentinel` — write access to mirror repos
- Git author: `Sentinel <sentinel@andusystems.com>` (configurable)
- Discord bot identity
- All actions tracked in SQLite `sentinel_actions` table

---

## Two-Worktree Model

**Forgejo worktree** (`workspace/<repo>/forgejo/`):
- Clone at Forgejo HEAD. Sentinel branches are created from here.
- Protected by a **per-repo read-write mutex** (`ForgejoWorktreeLock`):
  - Write lock: `git pull`, branch creation, [AI_ASSISTANT] Code invocation
  - Read lock: diff fetching for LLM analysis, Mode 2 review
- Never pushed directly to any Forgejo branch

**GitHub staging worktree** (`workspace/<repo>/github/`):
- Sanitized version; pushed directly to GitHub mirror (no PR)
- Protected by `FileMutexRegistry` per file for concurrent resolution writes

---

## Forgejo Webhook Events sentinel Subscribes To

sentinel registers the following event types on each watched Forgejo repo:

| Event | Used by |
|---|---|
| `pull_request` (opened, synchronized, closed, merged) | Mode 2 review trigger, Forgejo↔Discord sync |
| `push` | Detect [AI_ASSISTANT] Code branch pushes for PR opening |
| `issue_comment` (containing `/review`) | Mode 2 re-review trigger |

The webhook secret (`FORGEJO_WEBHOOK_SECRET`) is stored in a Kubernetes
Secret and validated via HMAC-SHA256 on every incoming event.

---

## Webhook Async Processing

**Forgejo webhooks must receive an HTTP 200 response quickly** (within
a few seconds) or Forgejo will mark the delivery as failed and may
retry. All webhook handlers in sentinel must:
1. Validate HMAC synchronously
2. Parse event type synchronously
3. Immediately return HTTP 200
4. Process the event asynchronously in a goroutine

This is critical for Mode 2 — [AI_ASSISTANT] Code invocations can take
several minutes. The webhook ACK and the actual work are fully decoupled.
A buffered event queue (in-memory channel, configurable size) between
the webhook handler and the processing goroutines handles this.

---

## LLM Context Management for Large Repos

When a repo diff exceeds the 16K context window of qwen2.5-coder:14b,
sentinel uses a **multi-call strategy** rather than truncating:

1. **Partition** the diff by file/module (go-git can produce per-file diffs)
2. **Prioritize** files matching the repo's configured `focus_areas`
   (e.g. `security`, `error-handling`) and files with the most changes
3. **Send batches** of files to the LLM — each call analyzes a subset
   and returns a partial `TaskSpec` JSON array
4. **Merge** the partial arrays, deduplicate by affected file + type,
   re-rank by priority
5. Cap the final merged list at `max_tasks_per_run`

Each individual LLM call must fit within `context_window - response_buffer`
(leave ~2K tokens for the response). Go measures token estimates using
a simple byte-count heuristic (1 token ≈ 4 bytes) before sending.

This approach is slower but produces higher-quality findings than
truncation, which would silently miss entire areas of the codebase.

---

## System Overview

`sentinel` has **four operating modes** plus a **Discord bot**, all
sharing DB, config, logging, and API clients.

---

### Mode 1 — Nightly SDLC Pipeline

Runs on cron (default 23:00). Per watched Forgejo repo.
`skip_if_active_dev_within` default: **2 hours** (configurable).

1. **Go: pre-flight** — git pull (write-lock Forgejo worktree), skip
   checks (active dev window, open sentinel PR flood threshold,
   excluded, migration pending)
2. **Local LLM: analysis** — multi-call diff analysis (see LLM Context
   Management section) → merged `TaskSpec` array
3. **Go: routing** — complexity/type → executor assignment
4. **Go: branch + PR per task** — for each task:
   - Create sentinel branch (write-lock worktree)
   - Invoke executor
   - Open PR, post Discord notification at appropriate priority tier
   - `SentinelPRStore.Create`
5. **Go: nightly pending digest** — if any open PRs or pending
   findings exist, post digest

Task routing:
- `type: bug|vulnerability`, `complexity: trivial|small` → [AI_ASSISTANT] Code → high PR
- `type: docs|changelog|readme|release-notes` → local LLM prose → low PR
- `type: dependency-update` → Go bumps manifest → low PR
- `type: issue` → Forgejo issue only (no PR)
- `complexity: large` → Forgejo issue, defer to developer
- `type: feature|refactor`, `complexity: small` → [AI_ASSISTANT] Code → high PR

---

### Mode 2 — PR Review Webhook

HTTP server. Triggers: `pull_request` opened/synchronized,
`issue_comment` with `/review`.

**Async processing** — webhook ACKs immediately, processes in goroutine.

1. HMAC validate, parse event, enqueue to processing channel
2. Return HTTP 200
3. [Async] Dedup check (cooldown window, configurable default 5 min)
4. [Async] Acquire read-lock on Forgejo worktree
5. [Async] Fetch PR diff via Forgejo API
6. [Async] Local LLM analysis → `ReviewResult` JSON
7. [Async] Post Forgejo review comment (verdict + analysis)
8. [Async] If housekeeping content:
   - Write-lock worktree, create sentinel branch, commit, push
   - Open PR targeting developer's base branch (NOT developer's branch)
   - Post low-priority Discord notification
   - Comment on developer's original PR with link
9. [Async] If code fix needed:
   - Write-lock worktree, create sentinel branch
   - Invoke [AI_ASSISTANT] Code (acquires global [AI_ASSISTANT] Code semaphore)
   - Sentinel detects [AI_ASSISTANT] Code's push via `push` webhook → opens PR
   - Post high-priority Discord notification
10. [Async] Create Forgejo issues for out-of-scope findings
11. [Async] Log to `sentinel_actions`

**When the developer merges their own PR** (detected via `pull_request`
closed+merged webhook where `head.ref` does NOT start with `sentinel/`):
- Check if there is an open housekeeping PR with `related_pr_number`
  matching this PR. If yes: add a comment to the housekeeping PR:
  "Original PR #N was merged. You can now merge this housekeeping PR
  at your convenience." Also surface it in the next nightly digest.

---

### Mode 3 — Forgejo → GitHub Incremental Sync

Runs after nightly pipeline or on separate schedule.
Direct push to GitHub mirror — no PR required.
3-layer sanitization on all changed files.
Per-finding sentinel tag commits push directly to GitHub.
`SyncRun` completes immediately with `complete` or `complete_with_pending`.

---

### Mode 4 — Initial Full-Repo Migration (One-Time Per Repo)

Operator-triggered: `sentinel migrate --repo <n> [--force]`.

**Handling existing GitHub repo**: if the GitHub repo already exists
and is non-empty:
- Without `--force`: abort, log error, Discord alert
- With `--force`: operator must explicitly confirm via Discord reaction
  before sentinel overwrites the repo (sentinel posts a confirmation
  embed; operator reacts ✅ to proceed)

Full 3-layer sanitization on HEAD snapshot → single squashed commit
to GitHub. Migration completes immediately. Subsequent findings
resolved via Discord reactions trigger individual per-finding commits.

---

### Discord Bot

Full bot via `github.com/bwmarrin/discordgo`.

Channels:
- **PR channel**: all PR notifications (both tiers), ✅/❌/💬 reactions
- **Findings channel**: sanitization findings, ✅/❌/🔍/✏️ reactions
- **Command channel**: text commands

Required Gateway Intents:
- `GatewayIntentGuildMessages`
- `GatewayIntentMessageContent`
- `GatewayIntentGuildMessageReactions`

#### PR Notification Reactions

**✅ Approve and merge**
- Validate operator user ID + guild ID
- `ForgejoProvider.MergePR(prNumber, FORGEJO_OPERATOR_TOKEN, strategy)`
- `SentinelPRStore.MarkMerged(id, userID)`
- Edit embed footer: `✅ Merged by <username> · <time>`
- If housekeeping PR: post comment on developer's original PR

**❌ Close**
- Validate operator user ID
- `ForgejoProvider.ClosePR(prNumber)` — sentinel token
- `SentinelPRStore.MarkClosed(id, userID)`
- Edit embed footer: `❌ Closed by <username> · <time>`

**💬 Discuss**
- Open thread under notification message
- Operator messages → local LLM (Role F) with PR diff context
- LLM responses posted in thread
- Resolution requires ✅ or ❌ on original message

**Forgejo-side resolution (merged or closed in Forgejo UI):**
Detected via `pull_request` webhook. Sentinel:
1. Identifies PR as sentinel-owned (`head.ref` starts with `sentinel/`)
2. Looks up `SentinelPR` by repo + PR number
3. If status already terminal: log, return (idempotent)
4. Updates `SentinelPRStore` to `merged` or `closed`
5. Edits Discord embed footer:
   - `✅ Merged in Forgejo · <time>` or `❌ Closed in Forgejo · <time>`
6. Posts a brief message to the PR channel:
   - `ℹ️ PR #<N> in <repo> was merged directly in Forgejo · <time>`
   - or `ℹ️ PR #<N> in <repo> was closed directly in Forgejo · <time>`

#### Sanitization Finding Reactions (unchanged from v11)

✅ approve, ❌ reject, 🔍 re-analyze, ✏️ edit/discuss.
Full token_index algorithm, per-finding GitHub pushes.

#### Nightly Pending Digest

Post at end of every nightly run if any pending items exist:
- Open high-priority PRs: each listed individually with title, age, link
- Open low-priority PRs: if ≤5, list individually; if >5, show count
  with "view all" link to avoid digest bloat
- Pending sanitization findings: count per repo, affected files, oldest age

#### Global Discord Commands (Command Channel)

- `/sentinel status` — open PRs by tier, pending findings, sync timestamps
- `/sentinel migrate <repo>` — trigger Mode 4
- `/sentinel skip <repo> <N>days` — pause nightly pipeline for repo
- `/sentinel sync <repo>` — trigger immediate Mode 3 sync
- `/sentinel dry-run <repo>` — analysis only, Discord output, no PRs
- `/sentinel allowlist <repo> <value>` — add to approved_values
  (requires ✅ confirmation, 10-minute TTL)
- `/sentinel findings <repo>` — list pending sanitization findings
- `/sentinel prs` — list all open sentinel PRs with tier and links
- `/sentinel prs <repo>` — filter by repo

---

## Sanitization Pipeline

Package `internal/sanitize`. Three-layer pipeline:

```
Forgejo worktree file
         │
         ▼
┌──────────────────────────────┐
│ approved_values pre-scan     │  Build byte-range skip zones
└──────────────┬───────────────┘
               ▼
┌──────────────────────────────┐
│ Layer 1: Go + gitleaks       │  Structural, fast, pattern-based
└──────────────┬───────────────┘  → category placeholder tokens applied
               ▼
┌──────────────────────────────┐
│ Layer 2: Local LLM           │  Semantic, context-aware
└──────────────┬───────────────┘  → category placeholder tokens applied
               ▼
┌──────────────────────────────┐
│ Layer 3: [AI_ASSISTANT] API          │  Final semantic safety net
└──────────────┬───────────────┘
     ┌──────────┴──────────┐
 high conf            medium/low conf
     │                     │
category placeholder   <REMOVED BY SENTINEL BOT: category — reason>
(silent, permanent)    + PendingResolution
                       + Discord finding message
                       + Forgejo issue
```

**Sentinel tag format** (plain inline text, no language syntax):
`<REMOVED BY SENTINEL BOT: <category> — <brief reason>>`
Category reasons from config `category_reasons` map. No `>` allowed
in reason strings (validated at config load). Multi-line values
collapsed to single line with count noted.

**token_index algorithm** for multi-finding files (per v11 spec):
Sequential scan by index, adjusted for resolved predecessors.
All resolution writes serialized by per-file mutex.
Per-finding GitHub commits: `chore(sync): sentinel resolved <category>
in <filename>:<line>`.

---

## Technology Stack

- **Language**: Go 1.22+
- **Scheduling**: `github.com/robfig/cron/v3`
- **Webhook server**: `net/http` stdlib (async ACK pattern)
- **Discord bot**: `github.com/bwmarrin/discordgo`
- **Forgejo API**: `code.gitea.io/sdk/gitea`
- **GitHub API**: `github.com/google/go-github/v66`
- **Git operations**: `github.com/go-git/go-git/v5`
- **Ollama**: `github.com/ollama/ollama/api`
- **[AI_ASSISTANT] API**: `net/http` direct to `/v1/messages`
- **[AI_ASSISTANT] Code CLI**: `os/exec` → `[AI_ASSISTANT]` binary
- **State DB**: `database/sql` + `modernc.org/sqlite`
- **Config**: `gopkg.in/yaml.v3` + `github.com/joho/godotenv`
- **Logging**: `log/slog`
- **Secret scanning**: `github.com/gitleaks/gitleaks/v8`
  (library if public API viable, subprocess fallback)
- **Deployment**: Kubernetes, ArgoCD, Helm, dedicated `sentinel`
  namespace, Longhorn RWO PVCs

---

## What to Produce

Output a complete plan as a Markdown document with ALL sections below.

---

### 1. Project Layout

Full directory and file tree. Every `.go` file, config file, prompt
template, Helm chart file, ArgoCD manifest, Makefile, Dockerfile.
One-line responsibility description per file.

Key packages:
- `internal/sanitize/` — 3-layer sanitization pipeline
- `internal/discord/` — bot lifecycle, reaction dispatch, threads
- `internal/worktree/` — two-worktree model, ForgejoWorktreeLock,
  sentinel tag placement, token_index algorithm, GitHub push
- `internal/prnotify/` — PR notification embeds, ✅/❌/💬 handlers,
  Forgejo↔Discord sync, operator token merge, channel message posting
- `internal/store/` — all DB operations
- `internal/forge/` — Forgejo and GitHub API clients
- `internal/llm/` — Ollama client, multi-call context management
- `internal/executor/` — [AI_ASSISTANT] Code CLI invocation
- `internal/pipeline/` — Mode 1 nightly SDLC
- `internal/webhook/` — HTTP server, async event queue, HMAC validation
- `internal/sync/` — Mode 3 incremental sync
- `internal/migration/` — Mode 4 initial migration

### 2. Go Module & Package Structure

- Module name
- All external dependencies: exact import paths + one-line justification
- Internal package breakdown: each package's responsibility, public
  surface, and explicit must-NOTs
- Package access rules:
  - Only `internal/sanitize` calls [AI_ASSISTANT] API
  - Only `internal/executor` invokes [AI_ASSISTANT] Code CLI
  - Only `internal/llm` calls Ollama
  - Only `internal/forge` makes Forgejo/GitHub API calls
  - Only `internal/discord` owns bot lifecycle and reaction dispatch
  - Only `internal/worktree` reads/writes worktree files and GitHub pushes
  - Only `internal/prnotify` calls Forgejo merge API with operator token
  - Only `internal/webhook` owns the HTTP server and event queue

### 3. LLM Routing Table

Complete exhaustive table. Four executor columns:
Go / Local LLM / [AI_ASSISTANT] API / [AI_ASSISTANT] Code CLI. Every sentinel action.

| Action | Executor | Rationale |
|--------|----------|-----------|

### 4. Configuration Schema

Complete annotated `config.yaml`. Include:
- Sentinel git identity (name, email)
- Forgejo/GitHub usernames for sentinel account
- `forgejo_operator_token` env var reference
- Discord: bot token ref, PR channel ID, findings channel ID, command
  channel ID, guild ID, operator user ID allowlist
- `pr.merge_strategy` (default `squash`, per-repo override)
- `pr.high_priority_types` list
- `pr.mention_on_security` bool
- `pr.mention_cooldown_minutes` (default 60)
- `pr.housekeeping.enabled` bool, `open_only_if_content` bool
- Sanitization: confidence thresholds, category placeholder tokens,
  `category_reasons` map, file skip patterns, [AI_ASSISTANT] API rate limits
- [AI_ASSISTANT] API model, max tokens, RPM
- Ollama: host, model, temperature, `context_window` (default 16384),
  `response_buffer_tokens` (default 2048)
- [AI_ASSISTANT] Code: binary path, flags, per-task timeout
- Worktree base path (PVC mount)
- `skip_if_active_dev_within_hours` (default 2)
- `webhook.event_queue_size` (default 100)
- `webhook.processing_workers` (default 4)
- Per-repo overrides: languages, focus areas, max tasks per run,
  migration status, sync enabled
- Repo exclusion list
- Nightly digest: enabled, low-priority PR collapse threshold (default 5)
- Allowlist confirmation TTL (default 10 min)

### 5. Database Schema

Full SQLite DDL. All tables on a **Longhorn RWO PVC** — specify this
constraint explicitly in the plan. SQLite requires exclusive file
access; RWX mounts with multiple writers would corrupt the DB.

```sql
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

CREATE TABLE sanitization_findings (
    id                    TEXT PRIMARY KEY,
    sync_run_id           TEXT NOT NULL REFERENCES sync_runs(id),
    layer                 INTEGER NOT NULL,
    repo                  TEXT NOT NULL,
    filename              TEXT NOT NULL,
    line_number           INTEGER NOT NULL,
    byte_offset_start     INTEGER NOT NULL,
    byte_offset_end       INTEGER NOT NULL,
    original_value        TEXT NOT NULL,
    suggested_replacement TEXT NOT NULL,
    category              TEXT NOT NULL,
    confidence            TEXT NOT NULL,
    auto_redacted         BOOLEAN NOT NULL DEFAULT FALSE,
    token_index           INTEGER,
    pending_resolution_id TEXT REFERENCES pending_resolutions(id)
);

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

CREATE TABLE approved_values (
    id          TEXT PRIMARY KEY,
    repo        TEXT NOT NULL,
    value       TEXT NOT NULL,
    category    TEXT NOT NULL,
    approved_by TEXT NOT NULL,
    approved_at DATETIME NOT NULL,
    UNIQUE(repo, value)
);

CREATE TABLE sync_runs (
    id                   TEXT PRIMARY KEY,
    repo                 TEXT NOT NULL,
    mode                 INTEGER NOT NULL,
    status               TEXT NOT NULL,
    started_at           DATETIME NOT NULL,
    completed_at         DATETIME,
    files_synced         INTEGER DEFAULT 0,
    files_with_pending   INTEGER DEFAULT 0,
    findings_high        INTEGER DEFAULT 0,
    findings_medium      INTEGER DEFAULT 0,
    findings_low         INTEGER DEFAULT 0
);

CREATE TABLE pending_confirmations (
    id                 TEXT PRIMARY KEY,
    kind               TEXT NOT NULL,
    repo               TEXT NOT NULL,
    value              TEXT,
    discord_message_id TEXT NOT NULL,
    discord_channel_id TEXT NOT NULL,
    requested_by       TEXT NOT NULL,
    created_at         DATETIME NOT NULL,
    expires_at         DATETIME NOT NULL
);
```

Also include full DDL for: `tasks`, `pr_dedup`, `reviews`,
`sync_records`, `webhook_events`, `sentinel_actions`,
`migration_status`.

### 6. Core Go Interfaces

Define as actual Go code blocks. All interfaces mockable.

```go
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
    // ResolveTag — caller MUST hold FileMutexRegistry lock
    ResolveTag(ctx context.Context, repo, filename string,
               tokenIndex, resolvedCount int, finalValue string) error
    // PushStagingFile — caller MUST hold FileMutexRegistry lock
    PushStagingFile(ctx context.Context, repo, filename, commitMsg string) error
    PushAllStaging(ctx context.Context, repo, commitMsg string) error
    SentinelTag(category string) string
}

// WebhookQueue decouples HTTP ACK from async event processing.
type WebhookQueue interface {
    Enqueue(event ForgejoEvent) error
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
    Repo             string
    Branch           string
    BaseBranch       string
    Title            string
    Body             string
    Labels           []string
    PRType           string
    PriorityTier     string
    RelatedPRNumber  int
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

// LLMClient wraps Ollama for all LLM roles.
type LLMClient interface {
    Analyze(ctx context.Context, opts AnalyzeOpts) ([]TaskSpec, error)
    ReviewPR(ctx context.Context, opts ReviewOpts) (*ReviewResult, error)
    WriteProse(ctx context.Context, opts ProseOpts) (string, error)
    SanitizeChunk(ctx context.Context, content string) ([]SanitizationFinding, error)
    AnswerThread(ctx context.Context, opts ThreadOpts) (string, error)
    WriteHousekeepingBody(ctx context.Context, opts HousekeepingOpts) (string, error)
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
    PostPRComment(ctx context.Context, repo string, prNumber int,
                  body string) error
    PostReview(ctx context.Context, repo string, prNumber int,
               verdict, body string) error
    ListOpenPRs(ctx context.Context, repo string) ([]ForgejoPR, error)
    GetWebhookEvents(ctx context.Context, repo string) ([]string, error)
}
```

### 7. Key Data Structures

Define as actual Go structs:

```go
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
    Type      string    // pull_request | push | issue_comment
    Repo      string
    Payload   []byte
    ReceivedAt time.Time
}

type NightlyDigest struct {
    HighPriorityPRs   []SentinelPR
    LowPriorityPRs    []SentinelPR
    PendingFindings   []PendingFindingDigest
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
    OriginalValue        string
    SuggestedReplacement string
    Category             string
    Confidence           string
    AutoRedacted         bool
    TokenIndex           int
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

type TaskSpec struct {
    ID                 string
    Type               string
    Priority           string
    Complexity         string
    Title              string
    AffectedFiles      []string
    Description        string
    AcceptanceCriteria []string
    ContextNotes       string
}

type ReviewResult struct {
    Verdict          string   // APPROVE | REQUEST_CHANGES | COMMENT
    PerFileNotes     []FileNote
    SecurityAssessment string
    TestAssessment   string
    ChangelogText    string
    DocUpdates       map[string]string // filename → updated content
    HousekeepingFiles map[string][]byte // filename → content for companion PR
    IssueSpecs       []IssueSpec
}
```

### 8. Local LLM Prompt Design

**Role A — Nightly Analyst**: per-file diff batch → JSON `TaskSpec` array.
Full schema, priority ordering, focus area weighting, scope constraints,
good/bad examples. Each call processes one batch of files; results merged
by Go. Max tasks cap applied after merge.

**Role B — PR Reviewer**: PR diff → structured JSON `ReviewResult`.
Includes verdict, per-file notes, security/test assessments, CHANGELOG
text, doc content updates, housekeeping files map, issue specs.

**Role C — Prose Writer**: targeted prose (issue title+body, README
section, dependency PR description, release notes). Returns plain text.

**Role D — Sanitization Semantic Pass**: file chunk (post-Layer-1) →
JSON findings array with confidence. Must not flag public libs or
clearly non-sensitive values.

**Role E — Finding Discussion Thread Responder**: finding details +
file chunk + operator question → plain-text answer.

**Role F — PR Discussion Thread Responder**: PR title + diff summary +
operator question → plain-text answer. Does NOT make merge decisions.

**Role G — Housekeeping PR Body**: developer PR title + diff summary +
list of housekeeping files changed → 2–3 sentence plain-text description
of what was updated and why.

For each role: full system prompt text, user prompt construction,
context truncation/batching strategy, response schema, malformed output
handling, retry behavior.

### 9. Go-Owned Operations — Implementation Spec

Specify completely:

**Multi-call LLM analysis** (Mode 1): partition diff by file using
go-git per-file diff API. Group files into batches where total estimated
tokens (bytes ÷ 4) fits within `context_window - response_buffer`.
Prioritize files matching `focus_areas` keywords. Send each batch as
a separate Ollama call. Merge and deduplicate returned `TaskSpec` arrays
by (`affected_files[0]`, `type`). Re-rank merged list by priority.
Truncate to `max_tasks_per_run`.

**Webhook async processing**: HTTP handler validates HMAC, parses
event type and repo, enqueues `ForgejoEvent` to buffered channel.
Returns HTTP 200 immediately. N worker goroutines (configurable, default
4) read from channel and dispatch to appropriate handler. Each worker
processes one event at a time. If queue is full: log warning, return
HTTP 429 (Forgejo will retry).

**token_index resolution algorithm**: full spec from v11 — scan for
`<REMOVED BY SENTINEL BOT:` occurrences, sorted ascending, adjust by
`resolvedCount`, replace at `targetPos`. Config validation ensures no
`>` in reason strings. Edge case: scan finds wrong count → error +
Discord alert.

**Sentinel tag string**: `<REMOVED BY SENTINEL BOT: %s — %s>` from
config `category_reasons` map.

**Staging content construction**: single left-to-right pass over findings
sorted by `byte_offset_start`. Assign `token_index` sequentially for
medium/low findings only.

**PR commit message**: `chore(sync): sentinel resolved <category> in
<base(filename)>:<line>`.

**Branch name**: `sentinel/<type>/<repo>/<slug>-<unix-ts>`. Slug:
lowercase, hyphens, max 40 chars. No LLM.

**PR title templates** by type (conventional commit format).

**PR body template**: fixed structure with LLM summary interpolated.
Housekeeping PRs include "Suggested workflow" and cherry-pick note.

**Housekeeping developer comment**: posted to original PR after
housekeeping PR is opened. Fixed format, Go-generated.

**Priority tier assignment**: config-driven `high_priority_types` list.

**Discord embed construction**: embed color and prefix emoji by tier.
`@here` mention for vulnerability type with cooldown check via DB
(`sentinel_actions` last mention timestamp).

**Forgejo↔Discord sync on webhook merge/close event**: look up
`sentinel_pr` by (repo, pr_number), check idempotency, update DB,
edit embed footer, post channel message.

**ForgejoWorktreeLock**: `sync.RWMutex` per repo, keyed in a
`map[string]*sync.RWMutex` behind a global mutex for map access.

**SQLite PVC constraint**: document explicitly — the DB file and
worktree directories MUST be on a Longhorn RWO (ReadWriteOnce) PVC,
not RWX. The Helm chart must set `accessModes: [ReadWriteOnce]`.

**`skip_if_active_dev_within_hours`**: check `git log --since=<N>h`
on the Forgejo worktree. If any non-sentinel commits exist within
the window, skip the nightly pipeline for that repo.

**All other operations**: branch naming, PR templates, CHANGELOG
mutation, label assignment, dependency detection, deduplication,
Forgejo issue creation — specify fully.

### 10. [AI_ASSISTANT] Code Task Spec Template

Go `text/template` for the Markdown prompt sent to [AI_ASSISTANT] Code via stdin:

```
## Task: {{.ID}}

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
Only change files listed above. Do NOT modify {{.BaseBranch}} directly.
Do NOT commit to any existing branch other than {{.BranchName}}.

## PR Instructions
- Title: {{.PRTitle}}
- Commit your changes to branch {{.BranchName}}
- Push the branch
- Do NOT open the PR yourself — sentinel will open it after your push
- Do NOT merge

## Git Author Identity
Use: Sentinel <sentinel@andusystems.com>
```

Note: sentinel opens the PR after detecting [AI_ASSISTANT] Code's push via the
Forgejo `push` webhook event. [AI_ASSISTANT] Code's only job is branch commits.

### 11. PR Review Webhook Flow (Mode 2)

1. Receive `pull_request` event → HMAC validate → enqueue → ACK HTTP 200
2. [Async worker] Check event type (opened / synchronized / comment `/review`)
3. Dedup check: skip if reviewed within cooldown (default 5 min, configurable)
4. Acquire read-lock on Forgejo worktree for this repo
5. Fetch PR diff via Forgejo API
6. Release read-lock
7. Local LLM (Role B) → `ReviewResult` JSON (multi-call if diff too large)
8. Post Forgejo PR review comment (verdict + analysis body)
9. If `ReviewResult.HousekeepingFiles` non-empty:
   a. Acquire write-lock on Forgejo worktree
   b. `PRCreator.CreateBranch` (`sentinel/sdlc/<repo>/pr<N>-...`)
   c. Local LLM (Role G) → housekeeping PR body prose
   d. `PRCreator.CommitAndPush` housekeeping files
   e. Release write-lock
   f. `PRCreator.OpenPR` (target = developer's base branch, tier = low)
   g. `PRNotifier.PostPRNotification` (low-priority embed)
   h. `SentinelPRStore.Create`
   i. Post housekeeping comment on developer's original PR
10. If code fix identified:
    a. Acquire write-lock
    b. `PRCreator.CreateBranch` (`sentinel/fix/<repo>/...`)
    c. Invoke [AI_ASSISTANT] Code via `TaskExecutor` (acquires global semaphore)
    d. Release write-lock ([AI_ASSISTANT] Code has the branch; it pushes async)
    e. [Later, via `push` webhook] Sentinel detects push → opens PR →
       posts high-priority notification → `SentinelPRStore.Create`
11. Create Forgejo issues for out-of-scope findings (Go + LLM prose Role C)
12. Log to `sentinel_actions`

### 12. Forgejo → GitHub Sync Flow (Mode 3)

1. Create `SyncRun` record, `running`
2. Acquire write-lock on Forgejo worktree, git pull, release lock
3. Acquire read-lock, identify changed files since last sync SHA, release lock
4. For each changed file:
   a. Check for existing pending findings → skip re-sanitization if any
   b. `ApprovedValuesStore.GetSkipZones`
   c. `SanitizationPipeline.SanitizeFile`
   d. `WorktreeManager.WriteGitHubStaging`
   e. For medium/low findings: `PendingResolutionStore.Create`,
      `DiscordBot.PostFinding`, `SeedFindingReactions`,
      `ForgejoProvider.CreateIssue`
5. `WorktreeManager.PushAllStaging` with initial sync commit
6. Update `SyncRun` to `complete` or `complete_with_pending`
7. Write `sync_records` with Forgejo HEAD SHA
8. `DiscordBot.PostChannelMessage` (run summary)

Subsequent per-finding resolution commits via reaction handlers.

### 13. Initial Migration Flow (Mode 4)

1. Pre-checks: GitHub repo existence check. If non-empty and no `--force`:
   abort + Discord alert. If `--force`: post confirmation embed, wait for
   ✅ reaction (uses `pending_confirmations` with TTL), then proceed.
2. Write-lock Forgejo worktree, full clone, release lock
3. For each file in HEAD snapshot:
   a. `GetSkipZones`, `SanitizeFile`, `WriteGitHubStaging`
   b. Create `PendingResolution` + Discord message + Forgejo issue for
      medium/low findings
4. `PushAllStaging` with squashed migration commit
5. Record HEAD SHA as sync baseline in `sync_records`
6. Update `migration_status`
7. Post Discord summary with token cost estimate vs actual
8. Mode 3 handles all future syncs

### 14. Concurrency Model

- **Nightly pipeline**: sequential repos (Ollama VRAM), sequential
  tasks per repo (same)
- **Webhook server**: N concurrent HTTP handlers (goroutines per
  connection); all processing deferred to async worker pool
- **Async event workers**: N goroutines reading from `WebhookQueue`
  channel (default 4). Each worker processes one event serially.
  Per-repo mutex prevents concurrent Mode 2 reviews for same repo.
- **Global [AI_ASSISTANT] Code semaphore**: max 1 concurrent invocation
- **Global Ollama semaphore**: max 1 concurrent request
- **[AI_ASSISTANT] API**: token bucket rate limiter (configurable RPM)
- **`ForgejoWorktreeLock`**: RW mutex per repo. Write lock for pull,
  branch creation, [AI_ASSISTANT] Code invocation. Read lock for diff reads.
- **Sanitization reaction handlers**: `FileMutexRegistry` lock covers
  `ResolveTag` + `PushStagingFile`. First-reaction-wins inside lock.
- **PR reaction handlers**: per-PR mutex. First-reaction-wins.
  Forgejo webhook merge/close: idempotent, no mutex needed.
- **Thread Q&A**: buffered channel → Ollama semaphore
- **Graceful shutdown**: drain webhook event queue, drain in-flight
  handlers, stop Discord bot, close DB, release worktree locks.
- **Context propagation**: all goroutines respect `ctx.Done()`.

### 15. Helm Chart Specification

Complete `charts/sentinel/` with all files. Critical constraints:

**PVC access mode: `ReadWriteOnce` (RWO) for BOTH PVCs.** SQLite
requires exclusive file access. Explicitly document this in chart.

Files:
- `Chart.yaml`, `values.yaml` (all configurable values)
- `templates/deployment.yaml`: both PVC mounts, all env vars, probes
- `templates/service.yaml`: webhook port ClusterIP
- `templates/configmap.yaml`: `config.yaml`, non-secret Discord config
- `templates/secret.yaml`: `FORGEJO_SENTINEL_TOKEN`,
  `FORGEJO_OPERATOR_TOKEN`, `DISCORD_BOT_TOKEN`, `ANTHROPIC_API_KEY`,
  `FORGEJO_WEBHOOK_SECRET`
- `templates/pvc-forgejo.yaml`: Longhorn RWO, forgejo workspace
- `templates/pvc-github.yaml`: Longhorn RWO, GitHub staging workspace
  (larger allocation — stores full repo HEAD snapshots)
- `templates/ingressroute.yaml`: Traefik IngressRoute (NOT standard
  Ingress) for webhook HTTPS endpoint with TLS termination
- `templates/certificate.yaml`: cert-manager Certificate, DNS-01
- `templates/networkpolicy.yaml`: egress allowlist — Forgejo host,
  `api.github.com`, Ollama host (internal cluster DNS),
  `api.[AI_PROVIDER].com`, `discord.com`, `gateway.discord.gg`
- `argocd/sentinel-app.yaml`: ArgoCD Application manifest

### 16. ArgoCD Application Manifest

Complete `argocd/sentinel-app.yaml`. **Manual sync** (not automated) —
sentinel has side effects on Forgejo, GitHub, and Discord; a bad deploy
auto-applying would be disruptive. Include ignore diffs for PVC status
and auto-generated annotation fields.

### 17. Makefile

Targets with one-line descriptions:
- `build` — compile sentinel binary
- `run` — run with schedule
- `dry-run` — analysis only, no PRs, no pushes, Discord output
- `webhook-test` — send synthetic Forgejo webhook events (PR open, push)
- `sync-dry-run` — sanitize and log, don't push to GitHub
- `llm-test` — send fixture diff to Ollama, print TaskSpec output
- `migrate` — trigger Mode 4 for a repo
- `migrate-dry-run` — full sanitization pass, print report, no GitHub push
- `sanitize-test` — run all three layers on fixture files
- `[AI_ASSISTANT]-api-test` — send fixture chunk to [AI_ASSISTANT] API, print findings
- `discord-test` — connect bot, post synthetic finding + PR notifications,
  verify reactions fire
- `reaction-test` — simulate all finding reactions (✅/❌/🔍/✏️)
- `pr-reaction-test` — simulate PR reactions (✅/❌/💬) and Forgejo
  webhook merge/close events, verify bidirectional sync
- `token-index-test` — unit tests for token_index algorithm only
- `forgejo-sync-test` — verify Forgejo→Discord embed updates on
  webhook merge/close events
- `install` — copy binary + Helm install
- `test` — run all Go tests
- `lint` — run golangci-lint
- `helm-lint` — helm lint charts/sentinel
- `docker-build` / `docker-push`

### 18. Testing Strategy

**Unit tests:**
- Config parsing: all fields, env var resolution, webhook secret,
  category reason `>` validation (must fail on bad input)
- **token_index algorithm** (highest priority): 1/3/5 findings,
  various resolution orders, `resolvedCount` computation, error paths
- Sentinel tag construction: all categories, multi-line variant
- Staging content construction: findings sorted, token_index assigned
- Commit message format: `resolutionCommitMsg`
- HMAC webhook validation
- Async webhook ACK: HTTP 200 returned before processing begins
- Webhook queue: enqueue/dequeue, full queue → HTTP 429
- `ForgejoWorktreeLock`: concurrent read/write goroutines serialized
- Each finding reaction handler: happy path, wrong-user, wrong-status,
  first-reaction-wins, per-file mutex
- Each PR reaction handler: happy path, wrong-user, already-merged
  idempotency, operator token used for ✅
- Forgejo webhook → Discord sync: merge event updates embed + posts
  channel message; close event does same; duplicate event is idempotent
- PR priority tier assignment: all `pr_type` values
- Discord embed construction: high/low tier colors and prefixes
- Multi-call LLM batching: partition logic, dedup + merge of results
- Thread message routing: token pattern, Q&A path
- approved_values pre-scan: skip zones correct

**Integration tests with mocks:**
- Full Mode 1 nightly: analysis → branch → PR → Discord notification
- Full Mode 2: PR webhook → review comment + housekeeping PR + code fix PR
- ✅ Discord → Forgejo merge → DB update → embed edit
- Forgejo merge → Discord embed update + channel message (sync)
- Forgejo close → Discord embed update + channel message (sync)
- ✅ Discord approval on already-Forgejo-merged PR → idempotent
- Full Mode 3 sync: clean files pushed, pending files have sentinel tags
- Full Mode 4 migration: `--force` confirmation flow, squashed push,
  pending findings tracked
- Multi-finding same file: resolve in random order, all correct
- Developer merges own PR → housekeeping PR gets notification comment

**Manual validation only:** Ollama output quality, [AI_ASSISTANT] API quality,
[AI_ASSISTANT] Code PR quality, Discord embed visual appearance.

### 19. Security Considerations

- **All tokens in Kubernetes Secrets**: never in config.yaml or logs.
  Document `FORGEJO_OPERATOR_TOKEN` rotation in operator runbook.
- **Token separation**: sentinel token never merges; operator token
  never creates content. Enforced by package access rules.
- **`FORGEJO_WEBHOOK_SECRET`**: HMAC-SHA256 on all incoming webhook
  events. Stored in Kubernetes Secret, configured in Forgejo per-repo
  webhook settings. Rotation requires updating both Forgejo and the Secret.
- **Webhook IP allowlist**: optionally restrict webhook endpoint to
  Forgejo server IP in NetworkPolicy/IngressRoute (document how to
  configure in operator runbook).
- **Async processing**: HTTP ACK is decoupled from processing; a
  malicious large payload cannot cause a slow handler path. Validate
  HMAC before enqueueing to prevent queue poisoning.
- **PR reaction validation**: `MergePR` failure (expired token) →
  log + Discord alert + do NOT mark merged. Operator retries or merges
  in Forgejo UI.
- **Branch protection**: enable Forgejo branch protection on `main`
  (and other protected branches) in all repos. Secondary safety net
  against any direct-push bugs.
- **[AI_ASSISTANT] Code branch isolation**: task spec forbids committing to
  existing branches. Branch protection is the enforcement backstop.
- **`original_value` in DB**: never logged. Read only inside per-file
  mutex for ❌ reject path.
- **Sentinel tag public on GitHub**: intentional, informative, no
  sensitive value exposed.
- **Custom token injection** (`^<[A-Z][A-Z0-9_]{0,98}>$`): pattern
  validated before any file write.
- **Longhorn RWO PVCs**: SQLite corruption prevention. Document in
  Helm chart comments: do not change `accessModes` to RWX.
- **`approved_values`**: ✅ confirmation + TTL + 256 char limit +
  audit log in `sentinel_actions`.

### 20. Open Questions & Decisions

Provide concrete recommended answers for each:

- **gitleaks library vs subprocess**: investigate `gitleaks/v8` public
  API. If `Detect()` or equivalent is accessible without subprocess,
  use library. Otherwise `gitleaks detect --no-git --report-format json`.
  Justify choice.

- **[AI_ASSISTANT] Code PR opening**: sentinel opens the PR after detecting
  [AI_ASSISTANT] Code's push via Forgejo `push` webhook. [AI_ASSISTANT] Code only
  commits to the branch. This keeps PR metadata (title, labels,
  description template, priority tier) fully under sentinel's control.

- **Housekeeping PR when developer's own PR is merged**: post a comment
  on the housekeeping PR: "Original PR #N was merged. This housekeeping
  can be merged independently." Surface in nightly digest.

- **Housekeeping PR when developer's own PR is closed without merge**:
  same as above — comment + nightly digest. The operator can close
  the housekeeping PR via ❌ reaction in Discord.

- **`@here` mention cooldown**: stored as last mention timestamp in
  `sentinel_actions`. Before posting `@here`, check if any `@here`
  action exists in `sentinel_actions` within `mention_cooldown_minutes`.

- **Mode 4 `--force` confirmation TTL**: 10 minutes, same as allowlist
  confirmation. If operator doesn't react in time, abort migration.

- **SQLite WAL mode**: enable WAL (Write-Ahead Logging) on the SQLite
  DB for better concurrent read performance (multiple goroutines reading
  while one writes). Set via `PRAGMA journal_mode=WAL` at DB open.
  Document this in the DB initialization code.

- **Forgejo webhook registration**: should sentinel auto-register
  webhooks on startup (via Forgejo API) or require manual setup?
  Recommend: auto-register on first run if webhook doesn't exist,
  idempotent. Use `FORGEJO_WEBHOOK_SECRET` from env. Document in
  operator runbook how to verify webhook registration.

- **Nightly pipeline `skip_if_active_dev` check**: check non-sentinel
  commits in the Forgejo worktree since N hours ago. Sentinel commits
  (author = `Sentinel <sentinel@andusystems.com>`) are excluded from
  this check — a sentinel housekeeping merge should not block the
  next nightly run.

---

Output the full plan as clean Markdown. Use fenced Go code blocks for
all interfaces and structs. Be exhaustive. When complete, output the
single line: `PLAN COMPLETE`
