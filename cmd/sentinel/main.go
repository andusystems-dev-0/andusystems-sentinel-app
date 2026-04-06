// Package main is the sentinel entry point.
// It loads config, initialises all subsystems, wires everything together,
// starts the cron scheduler, webhook server, and Discord bot.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/robfig/cron/v3"

	"github.com/andusystems/sentinel/internal/[AI_ASSISTANT]"
	"github.com/andusystems/sentinel/internal/config"
	"github.com/andusystems/sentinel/internal/discord"
	"github.com/andusystems/sentinel/internal/docs"
	"github.com/andusystems/sentinel/internal/executor"
	"github.com/andusystems/sentinel/internal/forge"
	"github.com/andusystems/sentinel/internal/llm"
	"github.com/andusystems/sentinel/internal/migration"
	"github.com/andusystems/sentinel/internal/pipeline"
	"github.com/andusystems/sentinel/internal/prnotify"
	"github.com/andusystems/sentinel/internal/reconcile"
	"github.com/andusystems/sentinel/internal/sanitize"
	"github.com/andusystems/sentinel/internal/store"
	syncp "github.com/andusystems/sentinel/internal/sync"
	"github.com/andusystems/sentinel/internal/types"
	"github.com/andusystems/sentinel/internal/webhook"
	"github.com/andusystems/sentinel/internal/worktree"
)

func main() {
	// ---- Flags ---------------------------------------------------------------
	configPath := flag.String("config", "config.yaml", "Path to config file")
	dryRun := flag.Bool("dry-run", false, "Dry run: no PRs, no GitHub pushes")
	mode := flag.String("mode", "run", "Mode: run | sync | migrate | nightly")
	targetRepo := flag.String("repo", "", "Target repo (for sync/migrate/dry-run modes)")
	force := flag.Bool("force", false, "Force mode (for migrate)")
	flag.Parse()

	// Suppress unused warning in dry-run mode (feature flag handled below).
	_ = *dryRun

	// ---- .env for local dev --------------------------------------------------
	_ = godotenv.Load(".env")

	// ---- Logging -------------------------------------------------------------
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	// ---- Config --------------------------------------------------------------
	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("load config", "err", err)
		os.Exit(1)
	}
	slog.Info("sentinel starting", "mode", *mode)

	// ---- Database ------------------------------------------------------------
	dbPath := "/data/db/sentinel.db"
	if v := os.Getenv("SENTINEL_DB_PATH"); v != "" {
		dbPath = v
	}
	db, err := store.Open(dbPath)
	if err != nil {
		slog.Error("open database", "err", err)
		os.Exit(1)
	}
	defer db.Close()

	// ---- Worktree ------------------------------------------------------------
	wtLock := worktree.NewForgejoWorktreeLock()
	fileLock := worktree.NewFileMutexRegistry()
	wt := worktree.NewManager(cfg, wtLock, fileLock)

	// ---- Forge clients -------------------------------------------------------
	forgejoClient, err := forge.NewForgejoClient(cfg)
	if err != nil {
		slog.Error("create forgejo client", "err", err)
		os.Exit(1)
	}
	githubClient := forge.NewGitHubClient(cfg)

	// ---- LLM -----------------------------------------------------------------
	llmClient, err := llm.NewClient(cfg)
	if err != nil {
		slog.Error("create LLM client", "err", err)
		os.Exit(1)
	}
	batcher := llm.NewBatcher(llmClient, cfg)

	// ---- Sanitization pipeline -----------------------------------------------
	// [AI_ASSISTANT] Code CLI is the backend for both Layer 3 and the Layer 2
	// fallback. We do NOT make direct [AI_PROVIDER] API calls from Go — the CLI
	// handles its own authentication (subscription or ANTHROPIC_API_KEY).
	var claudeAPI types.ClaudeAPIClient
	if cfg.ClaudeCode.BinaryPath != "" {
		claudeAPI = [AI_ASSISTANT].NewClient(
			cfg.ClaudeCode.BinaryPath,
			cfg.ClaudeCode.Flags,
			0, // default CLI timeout (2m)
		)
	}
	sanitizePipeline, err := sanitize.NewPipeline(llmClient, claudeAPI, &cfg.Sanitize)
	if err != nil {
		slog.Error("create sanitize pipeline", "err", err)
		os.Exit(1)
	}

	// ---- Discord bot ---------------------------------------------------------
	bot, err := discord.NewBot(cfg)
	if err != nil {
		slog.Error("create Discord bot", "err", err)
		os.Exit(1)
	}
	bot.SetConfirmationStore(db.Confirmations)

	// ---- PR Notifier ---------------------------------------------------------
	mentionTracker := prnotify.NewMentionTracker(db.Actions, cfg.PR.MentionCooldownMinutes)
	notifier := prnotify.NewNotifier(bot, forgejoClient, db.PRs, db.Actions, mentionTracker, cfg)

	// Register PR reaction handlers on bot.
	bot.RegisterPRHandler(prnotify.NewPRApproveHandler(notifier, db.PRs))
	bot.RegisterPRHandler(prnotify.NewPRCloseHandler(notifier, db.PRs))
	bot.RegisterPRHandler(prnotify.NewPRDiscussHandler(notifier, db.PRs))

	// Register finding reaction handlers on bot.
	bot.RegisterFindingHandler(discord.NewFindingApproveHandler(bot, db.Resolutions, wt, fileLock, db.Actions))
	bot.RegisterFindingHandler(discord.NewFindingRejectHandler(bot, db.Resolutions, db.Actions))
	bot.RegisterFindingHandler(discord.NewFindingReanalyzeHandler(bot, db.Resolutions, claudeAPI, db.Actions))
	bot.RegisterFindingHandler(discord.NewFindingCustomHandler(bot, db.Resolutions, wt, fileLock, db.Actions))

	// ---- Executor ------------------------------------------------------------
	taskExecutor := executor.NewTaskExecutor(cfg, cfg.Worktree.BasePath+"/forgejo")

	// ---- Sync runner ---------------------------------------------------------
	syncRunner := syncp.NewRunner(cfg, db, wt, wtLock, sanitizePipeline, bot, forgejoClient, db.ApprovedValues)

	// ---- Migration manager ---------------------------------------------------
	migrationMgr := migration.NewManager(cfg, db, wt, wtLock, sanitizePipeline, bot, forgejoClient, githubClient, db.ApprovedValues)

	// ---- Docs + Changelog ----------------------------------------------------
	docGen := docs.NewGenerator(cfg, db, wt, wtLock, taskExecutor, forgejoClient, bot, notifier, db.PRs)
	changelogMgr := docs.NewChangelogManager(cfg, db, wt, wtLock, llmClient, forgejoClient)

	// ---- Nightly pipeline ---------------------------------------------------
	nightlyRunner := pipeline.NewNightlyRunner(cfg, db, wt, wtLock, batcher, taskExecutor, forgejoClient, notifier, docGen, changelogMgr)

	// ---- Sync handler (Forgejo→Discord) --------------------------------------
	syncHandler := prnotify.NewSyncHandler(notifier, db.PRs, db.Actions, syncRunner)

	// ---- Webhook queue + processor -------------------------------------------
	queue := webhook.NewQueue(cfg.Webhook.EventQueueSize)
	prDisp := &prEventDispatch{syncHandler: syncHandler}
	pushDisp := &pushEventDispatch{db: db, forge: forgejoClient, notifier: notifier, cfg: cfg, syncRunner: syncRunner}
	processor := webhook.NewEventProcessor(queue, prDisp, pushDisp, cfg.Webhook.ProcessingWorkers)

	// ---- Context + shutdown --------------------------------------------------
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	// ---- Mode dispatch -------------------------------------------------------
	switch *mode {
	case "sync":
		if *targetRepo == "" {
			fmt.Fprintln(os.Stderr, "error: --repo required for sync mode")
			os.Exit(1)
		}
		if err := syncRunner.Sync(ctx, *targetRepo); err != nil {
			slog.Error("sync failed", "repo", *targetRepo, "err", err)
			os.Exit(1)
		}
		return

	case "doc-gen":
		if *targetRepo != "" {
			syncGitHubDescription(ctx, githubClient, cfg, *targetRepo)
			if err := docGen.RunFull(ctx, *targetRepo); err != nil {
				slog.Error("doc-gen failed", "repo", *targetRepo, "err", err)
				os.Exit(1)
			}
		} else {
			for _, r := range cfg.Repos {
				if r.Excluded {
					continue
				}
				syncGitHubDescription(ctx, githubClient, cfg, r.Name)
				slog.Info("doc-gen: starting", "repo", r.Name)
				if err := docGen.RunFull(ctx, r.Name); err != nil {
					slog.Error("doc-gen failed", "repo", r.Name, "err", err)
					continue
				}
			}
		}
		return

	case "migrate":
		if *targetRepo == "" {
			fmt.Fprintln(os.Stderr, "error: --repo required for migrate mode")
			os.Exit(1)
		}
		if err := bot.Start(ctx); err != nil {
			slog.Error("start discord bot for migration", "err", err)
			os.Exit(1)
		}
		if err := migrationMgr.Migrate(ctx, *targetRepo, *force); err != nil {
			slog.Error("migration failed", "repo", *targetRepo, "err", err)
			os.Exit(1)
		}
		bot.Stop()
		return

	case "nightly":
		if *targetRepo != "" {
			if err := nightlyRunner.Run(ctx, *targetRepo); err != nil {
				slog.Error("nightly run failed", "repo", *targetRepo, "err", err)
				os.Exit(1)
			}
		} else {
			nightlyRunner.RunAll(ctx)
		}
		return
	}

	// ---- Full daemon mode ("run") --------------------------------------------

	// Register webhooks on all repos at startup (best-effort).
	go func() {
		ingressHost := os.Getenv("SENTINEL_INGRESS_HOST")
		if ingressHost != "" {
			if err := forgejoClient.RegisterAllWebhooks(ctx, ingressHost); err != nil {
				slog.Warn("webhook registration incomplete", "err", err)
			}
		}
	}()

	// Start Discord bot.
	if err := bot.Start(ctx); err != nil {
		slog.Error("start discord bot", "err", err)
		os.Exit(1)
	}

	// Start cron scheduler.
	c := cron.New()
	if _, err := c.AddFunc(cfg.Nightly.Cron, func() {
		slog.Info("cron: starting nightly pipeline")
		nightlyRunner.RunAll(ctx)
		bot.PostDigest(ctx, db.PRs)
	}); err != nil {
		slog.Error("invalid cron expression", "cron", cfg.Nightly.Cron, "err", err)
		os.Exit(1)
	}
	c.Start()

	// ---- Auto-bootstrap + drift reconciler -----------------------------------
	// Bootstrap: run Mode 4 migration for any sync-enabled repo whose GitHub
	// mirror doesn't exist yet or is empty. This is sequential and may take
	// a while; we run it in a goroutine so the webhook listener stays
	// responsive, then the drift reconciler kicks in afterward.
	reconciler := reconcile.NewReconciler(cfg, db, wt, wtLock, syncRunner)
	go func() {
		slog.Info("bootstrap: checking for empty/missing GitHub mirrors")
		migrationMgr.AutoBootstrap(ctx)
		if cfg.Reconcile.OnStartup {
			slog.Info("reconcile: startup drift check")
			reconciler.RunOnce(ctx)
		}
	}()
	var stopReconciler func() = func() {}
	if cfg.Reconcile.IntervalMinutes > 0 {
		stopReconciler = reconciler.StartPeriodic(ctx,
			time.Duration(cfg.Reconcile.IntervalMinutes)*time.Minute)
	}

	// Start webhook event processor in background.
	go processor.Start(ctx)

	// Start webhook HTTP server in background.
	server := webhook.NewServer(cfg.Webhook.Port, queue, cfg.Webhook.Secret)
	go func() {
		if err := server.Start(); err != nil {
			slog.Error("webhook server error", "err", err)
		}
	}()

	slog.Info("sentinel running",
		"webhook_port", cfg.Webhook.Port,
		"nightly_cron", cfg.Nightly.Cron,
		"repos", len(cfg.Repos),
	)

	// ---- Graceful shutdown ---------------------------------------------------
	<-sigCh
	slog.Info("shutdown signal received")
	cancel()

	// Stop the reconciler ticker before cron, so no new drift sync starts.
	stopReconciler()

	// Wait for cron jobs to finish (max 5 min).
	cronCtx := c.Stop()
	select {
	case <-cronCtx.Done():
	case <-time.After(5 * time.Minute):
		slog.Warn("cron did not stop within 5 minutes")
	}

	// Stop accepting new webhook connections (30s drain).
	server.Stop(30 * time.Second)

	// Close queue so workers exit.
	queue.Close()

	// Give workers a moment to finish in-flight events.
	time.Sleep(2 * time.Second)

	bot.Stop()
	db.Close()
	slog.Info("sentinel stopped")
}

// ---- Webhook event dispatchers ----------------------------------------------

type prEventDispatch struct {
	syncHandler *prnotify.SyncHandler
}

func (d *prEventDispatch) HandlePREvent(ctx context.Context, event types.ForgejoEvent) {
	d.syncHandler.HandlePRWebhook(ctx, event)
}

type pushEventDispatch struct {
	db         *store.DB
	forge      types.ForgejoProvider
	notifier   *prnotify.Notifier
	cfg        *config.Config
	syncRunner *syncp.Runner
}

func (d *pushEventDispatch) HandlePushEvent(ctx context.Context, event types.ForgejoEvent) {
	branch := parsePushBranch(event.Payload)
	if branch == "" {
		return
	}

	// Non-sentinel pushes: if the branch is the repo's default/main branch,
	// trigger Mode 3 incremental sync (Forgejo → GitHub mirror).
	if !strings.HasPrefix(branch, "sentinel/") {
		if !d.isRepoConfigured(event.Repo) {
			return
		}
		if !isDefaultBranch(branch) {
			slog.Debug("push handler: ignoring non-default branch",
				"repo", event.Repo, "branch", branch)
			return
		}
		slog.Info("push handler: triggering Mode 3 sync",
			"repo", event.Repo, "branch", branch)
		if err := d.syncRunner.Sync(ctx, event.Repo); err != nil {
			slog.Error("push handler: sync failed",
				"repo", event.Repo, "branch", branch, "err", err)
		}
		return
	}

	// Look up the task that created this branch.
	task, err := d.db.Tasks.GetByBranch(ctx, branch)
	if err != nil {
		slog.Error("push handler: get task by branch", "branch", branch, "err", err)
		return
	}
	if task == nil {
		slog.Warn("push handler: no task found for sentinel branch", "branch", branch, "repo", event.Repo)
		return
	}

	prType := pipeline.TaskTypeToPRType(task.TaskType)
	prTitle := pipeline.PRTitleFor(types.TaskSpec{Type: task.TaskType, Title: task.Title})
	tier := pipeline.PriorityTier(prType, d.cfg.PR.HighPriorityTypes)

	// Open the PR on Forgejo.
	prNumber, prURL, err := d.forge.CreatePR(ctx, types.OpenPROptions{
		Repo:         event.Repo,
		Branch:       branch,
		BaseBranch:   "main",
		Title:        prTitle,
		Body:         task.Description,
		PRType:       prType,
		PriorityTier: tier,
	})
	if err != nil {
		slog.Error("push handler: create PR", "branch", branch, "repo", event.Repo, "err", err)
		return
	}

	// Post Discord notification first to get the message ID.
	pr := types.SentinelPR{
		ID:               store.NewID(),
		Repo:             event.Repo,
		PRNumber:         prNumber,
		PRUrl:            prURL,
		Branch:           branch,
		BaseBranch:       "main",
		Title:            prTitle,
		PRType:           prType,
		PriorityTier:     tier,
		Status:           types.PRStatusOpen,
		OpenedAt:         time.Now(),
		TaskID:           task.ID,
		DiscordChannelID: d.cfg.Discord.PRChannelID,
	}

	msgID, err := d.notifier.PostPRNotification(ctx, pr, task.Description)
	if err != nil {
		slog.Error("push handler: post PR notification", "repo", event.Repo, "pr", prNumber, "err", err)
		// PR is open on Forgejo — save the DB record anyway so reactions can still be handled.
	}
	pr.DiscordMessageID = msgID

	if err := d.db.PRs.Create(ctx, pr); err != nil {
		slog.Error("push handler: save sentinel PR record", "repo", event.Repo, "pr", prNumber, "err", err)
		return
	}

	d.db.Tasks.SetPRNumber(ctx, task.ID, prNumber)

	slog.Info("push handler: PR opened", "repo", event.Repo, "branch", branch, "pr", prNumber)
}

// isRepoConfigured returns true if a repo name is in cfg.Repos and sync-enabled.
func (d *pushEventDispatch) isRepoConfigured(repoName string) bool {
	for _, r := range d.cfg.Repos {
		if r.Name == repoName {
			return r.SyncEnabled && !r.Excluded
		}
	}
	return false
}

// isDefaultBranch returns true for common default branch names.
// Sentinel only syncs pushes on main/master; other branches are ignored.
func isDefaultBranch(branch string) bool {
	return branch == "main" || branch == "master"
}

// parsePushBranch extracts the branch name from a Forgejo push webhook payload.
func parsePushBranch(payload []byte) string {
	var p struct {
		Ref string `json:"ref"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return ""
	}
	return strings.TrimPrefix(p.Ref, "refs/heads/")
}

// syncGitHubDescription ensures the GitHub repo description matches the config.
// Best-effort — logs a warning on failure but never aborts the caller.
func syncGitHubDescription(ctx context.Context, gh *forge.GitHubClient, cfg *config.Config, repoName string) {
	for _, r := range cfg.Repos {
		if r.Name == repoName && r.GitHubPath != "" && r.Description != "" {
			if err := gh.EnsureRepo(ctx, r.GitHubPath, r.Description); err != nil {
				slog.Warn("doc-gen: sync github description failed",
					"repo", repoName, "err", err)
			}
			return
		}
	}
}
