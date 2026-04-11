# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Added
- Full scan option for nightly runs via Discord bot `/sentinel nightly --force` command
- `GitLogsChannelID` Discord configuration for dedicated git operation logging
- Dedicated PR channel (`pr_channel_id`) with fallback to actions channel
- Nightly session management with session budget tracking
- Discord slash command handling for `/sentinel` commands (nightly, status)
- REST API (`/api/v1/`) for sessions, tasks, PRs, actions, and repo listing
- Server-Sent Events endpoint (`/api/v1/events`) for real-time dashboard updates
- Embedded SvelteKit web dashboard with live session monitoring
- `EventBus` fan-out for non-blocking SSE event distribution
- SPA handler with client-side routing support and cache-optimized static assets
- Drift reconciliation: periodic checks catch missed webhooks and trigger Mode 3 sync
- Auto-bootstrap: automatically runs Mode 4 migration for sync-enabled repos with missing or empty GitHub mirrors on daemon startup
- [AI_ASSISTANT] Code CLI integration for sanitization Layer 2 fallback and Layer 3
- GitHub repository management (create, ensure description, visibility sync)
- GitHub identity configuration (`github.git_name`, `github.git_email`) for mirror commit authorship
- GitHub description synchronization in doc-gen command
- Scrub patterns (`sanitize.scrub_patterns`) for regex-based content substitution before mirroring
- Layer 2 timeout with [AI_ASSISTANT] Code CLI fallback (`sanitize.layer2_timeout_seconds`)
- Configurable Layer 3 enable/disable (`sanitize.layer3_enabled`)
- Documentation generation mode (`--mode doc-gen`) with Obsidian vault integration
- Changelog management (`internal/docs/changelog.go`) with LLM-assisted generation
- Obsidian vault snapshot writing for generated documentation
- `ReconcileConfig` for startup and periodic drift detection
- `DocGenConfig` for controlling documentation generation targets and context limits
- `ObsidianConfig` for vault path and directory structure
- Session budget minutes configuration (`nightly.session_budget_minutes`)
- Mention tracking with cooldown for Discord @here notifications
- Full daemon mode with webhook server, Discord bot, cron scheduler, drift reconciler, and REST API
- Mode 1 -- Nightly pipeline: cron-driven code analysis via Ollama, task routing, PR creation
- Mode 2 -- PR Review: webhook-driven LLM review with verdict, per-file notes, housekeeping PRs
- Mode 3 -- Incremental sync: sanitize changed files and push to GitHub mirror
- Mode 4 -- Full migration: one-time complete repo scan, sanitization, and GitHub mirror push
- Three-layer sanitization pipeline (gitleaks, Ollama semantic analysis, [AI_ASSISTANT] Code CLI safety net)
- Discord bot with operator-gated emoji reactions for merge, close, approve, reject, re-analyse
- PR notification system with Forgejo-to-Discord synchronisation
- Webhook HTTP server with HMAC-SHA256 validation and async worker pool
- SQLite database with WAL mode, single-writer constraint, and idempotent migrations
- Per-repo git worktree management with RW locking
- Token index algorithm for offset-aware sanitisation tag resolution
- Seven LLM roles (Analyst, Reviewer, Prose, Sanitize, Finding thread, PR thread, Housekeeping)
- Diff batching for large files exceeding LLM context windows
- Nightly digest summaries posted to Discord
- Helm chart for Kubernetes deployment with RWO PVCs
- ArgoCD Application manifest (manual sync only)
- Docker multi-stage build (non-root container)
- Health and readiness endpoints (`/health`, `/ready`)
- Append-only audit log (`sentinel_actions` table)
- Graceful shutdown with cron drain, webhook drain, and WAL checkpoint

### Changed
- Refactored Discord channel handling to support separate PR, actions, logs, and git-logs channels
- Nightly runner now tracks sessions with start/end times and publishes SSE events for dashboard
- Removed budget filtering from nightly runner tasks (session budget is advisory, not enforced as a hard cap)
- Sanitization Layer 3 now uses [AI_ASSISTANT] Code CLI instead of direct [AI_PROVIDER] API calls

### Fixed
- Sync status messaging now correctly reports findings count and push result
- Error handling improvements in sync pipeline for partial failures
- `RenderTaskSpec` function signature updated for consistency
