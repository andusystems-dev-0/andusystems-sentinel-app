# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Changed
- README.md: Added detailed per-option configuration reference covering all config.yaml sections
- README.md: Added missing `digest`, `allowlist`, and `excluded_repos` sections to config summary table

### Added
- GitHub identity configuration and commit message formatting for sync operations
- GitHub description synchronization to doc-gen command
- Drift reconciliation: periodic checks catch missed webhooks and trigger Mode 3 sync
- Auto-bootstrap: automatically runs Mode 4 migration for sync-enabled repos with missing or empty GitHub mirrors on daemon startup
- [AI_ASSISTANT] API integration for sanitization Layer 3
- GitHub repository management (create, ensure description, visibility)
- Full daemon mode with webhook server, Discord bot, cron scheduler, and drift reconciler
- Mode 1 — Nightly pipeline: cron-driven code analysis via Ollama, task routing, PR creation
- Mode 2 — PR Review: webhook-driven LLM review with verdict, per-file notes, housekeeping PRs
- Mode 3 — Incremental sync: sanitize changed files and push to GitHub mirror
- Mode 4 — Full migration: one-time complete repo scan, sanitization, and GitHub mirror push
- Three-layer sanitization pipeline (gitleaks, Ollama semantic analysis, [AI_ASSISTANT] API safety net)
- Discord bot with operator-gated emoji reactions for merge, close, approve, reject, re-analyse
- PR notification system with Forgejo-to-Discord synchronisation
- Webhook HTTP server with HMAC-SHA256 validation and async worker pool
- SQLite database with WAL mode, single-writer constraint, and idempotent migrations
- Per-repo git worktree management with RW locking
- Token index algorithm for offset-aware sanitisation tag resolution
- Seven LLM roles (Analyst, Reviewer, Prose, Sanitize, Finding thread, PR thread, Housekeeping)
- Diff batching for large files exceeding LLM context windows
- Nightly digest summaries posted to Discord
- Helm chart for Kubernetes deployment with Longhorn RWO PVCs
- ArgoCD Application manifest (manual sync only)
- Docker multi-stage build (non-root container)
- Health and readiness endpoints (`/health`, `/ready`)
- Append-only audit log (`sentinel_actions` table)
- Documentation generation command (`--mode doc-gen`)
- Graceful shutdown with cron drain, webhook drain, and WAL checkpoint
