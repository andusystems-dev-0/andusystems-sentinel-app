package pipeline

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/andusystems/sentinel/internal/config"
	"github.com/andusystems/sentinel/internal/docs"
	"github.com/andusystems/sentinel/internal/executor"
	"github.com/andusystems/sentinel/internal/llm"
	"github.com/andusystems/sentinel/internal/prnotify"
	"github.com/andusystems/sentinel/internal/store"
	"github.com/andusystems/sentinel/internal/types"
	"github.com/andusystems/sentinel/internal/worktree"
)

// complexityBudget maps task complexity to estimated minutes (plan + impl).
var complexityBudget = map[string]int{
	"trivial": 10,
	"small":   20,
	"medium":  40,
	"large":   60,
}

// complexityTimeout maps task complexity to [AI_ASSISTANT] Code timeout minutes.
var complexityTimeout = map[string]int{
	"trivial": 8,
	"small":   15,
	"medium":  30,
	"large":   45,
}

// NightlyRunner orchestrates Mode 1 nightly SDLC for all watched repos.
type NightlyRunner struct {
	cfg       *config.Config
	db        *store.DB
	wt        *worktree.Manager
	wtLock    types.ForgejoWorktreeLock
	batcher   *llm.Batcher
	executor  *executor.TaskExecutor
	forge     types.ForgejoProvider
	notifier  *prnotify.Notifier
	discord   types.DiscordBot
	docGen    *docs.Generator
	changelog *docs.ChangelogManager

	cancelMu sync.Mutex
	cancelFn context.CancelFunc

	// eventPublisher is called for each SSE event. Set via SetEventPublisher.
	eventPublisher func(eventType string, data any)
}

// NewNightlyRunner creates a NightlyRunner.
func NewNightlyRunner(
	cfg *config.Config,
	db *store.DB,
	wt *worktree.Manager,
	wtLock types.ForgejoWorktreeLock,
	batcher *llm.Batcher,
	exec *executor.TaskExecutor,
	forge types.ForgejoProvider,
	notifier *prnotify.Notifier,
	discord types.DiscordBot,
	docGen *docs.Generator,
	changelog *docs.ChangelogManager,
) *NightlyRunner {
	return &NightlyRunner{
		cfg:       cfg,
		db:        db,
		wt:        wt,
		wtLock:    wtLock,
		batcher:   batcher,
		executor:  exec,
		forge:     forge,
		notifier:  notifier,
		discord:   discord,
		docGen:    docGen,
		changelog: changelog,
	}
}

// SetEventPublisher sets a callback for publishing SSE events to the dashboard.
func (r *NightlyRunner) SetEventPublisher(fn func(eventType string, data any)) {
	r.eventPublisher = fn
}

// publishEvent sends an event to connected dashboard clients (if any).
func (r *NightlyRunner) publishEvent(eventType string, data any) {
	if r.eventPublisher != nil {
		r.eventPublisher(eventType, data)
	}
}

// Stop cancels the active nightly session gracefully. The in-flight task
// finishes but no new tasks are started.
func (r *NightlyRunner) Stop() {
	r.cancelMu.Lock()
	defer r.cancelMu.Unlock()
	if r.cancelFn != nil {
		r.cancelFn()
	}
}

// RunAll runs the nightly pipeline for all non-excluded repos sequentially.
func (r *NightlyRunner) RunAll(ctx context.Context) error {
	for _, repo := range r.cfg.Repos {
		if repo.Excluded || !repo.SyncEnabled {
			continue
		}

		if err := r.Run(ctx, repo.Name); err != nil {
			slog.Error("nightly run failed", "repo", repo.Name, "err", err)
			// Continue to next repo — non-blocking.
		}
	}
	return nil
}

// RunAllFull runs a full-scan nightly pipeline for all non-excluded repos sequentially.
func (r *NightlyRunner) RunAllFull(ctx context.Context) error {
	for _, repo := range r.cfg.Repos {
		if repo.Excluded || !repo.SyncEnabled {
			continue
		}
		if err := r.RunFull(ctx, repo.Name); err != nil {
			slog.Error("nightly full run failed", "repo", repo.Name, "err", err)
		}
	}
	return nil
}

// Run executes the nightly pipeline for a single repo (incremental — only
// files changed since last sync SHA).
func (r *NightlyRunner) Run(ctx context.Context, repoName string) error {
	return r.RunWithOpts(ctx, repoName, false)
}

// RunFull executes the nightly pipeline against the entire codebase, ignoring
// the last-synced baseline. Used with --mode nightly --force to generate
// feature/improvement suggestions from the full repo.
func (r *NightlyRunner) RunFull(ctx context.Context, repoName string) error {
	return r.RunWithOpts(ctx, repoName, true)
}

// RunWithOpts is the shared implementation. When fullScan is true, all files
// in HEAD are analyzed regardless of the last-synced SHA.
//
// Execution proceeds in three phases:
//  1. Analysis — LLM identifies tasks from file diffs
//  2. Planning — LLM generates detailed implementation plans per task
//  3. Implementation — [AI_ASSISTANT] Code executes each plan on a feature branch
func (r *NightlyRunner) RunWithOpts(ctx context.Context, repoName string, fullScan bool) error {
	// Create a cancellable child context for stop support.
	ctx, cancel := context.WithCancel(ctx)
	r.cancelMu.Lock()
	r.cancelFn = cancel
	r.cancelMu.Unlock()
	defer cancel()

	budget := r.cfg.Nightly.SessionBudgetMinutes

	slog.Info("nightly pipeline start", "repo", repoName, "full_scan", fullScan, "budget_min", budget)

	// ---- Pre-flight checks (before creating a session or posting to Discord) ----
	r.wtLock.Lock(repoName)
	if err := r.wt.EnsureForgejoWorktree(ctx, repoName); err != nil {
		r.wtLock.Unlock(repoName)
		return fmt.Errorf("ensure worktree: %w", err)
	}
	r.wtLock.Unlock(repoName)

	repoConfig := r.repoConfig(repoName)
	if repoConfig == nil {
		return fmt.Errorf("repo %q not found in config", repoName)
	}

	openPRs, err := r.db.PRs.GetOpenPRsForRepo(ctx, repoName)
	if err != nil {
		return err
	}

	skip, reason, err := PreflightCheck(
		ctx, repoName, r.wt.ForgejoDir(repoName), openPRs,
		r.cfg.Nightly.FloodThreshold,
		r.cfg.Nightly.SkipIfActiveDevWithinHours,
		r.cfg.Sentinel.GitEmail,
	)
	if err != nil {
		return err
	}
	if skip {
		slog.Info("nightly pipeline skipped", "repo", repoName, "reason", reason)
		return nil
	}

	var lastSHA string
	if !fullScan {
		lastSHA, err = r.db.SyncRuns.GetRepoSyncSHA(ctx, repoName)
		if err != nil {
			return err
		}
	}

	diffs, currentSHA, err := r.getFileDiffs(ctx, repoName, lastSHA)
	if err != nil {
		return fmt.Errorf("get diffs for %s: %w", repoName, err)
	}

	if len(diffs) == 0 {
		slog.Info("nightly pipeline: no changes since last run", "repo", repoName)
		return nil
	}

	// ---- There is work to do — create session and notify Discord ----
	sessionStart := time.Now()

	session := store.NightlySession{
		ID:            newID(),
		Repo:          repoName,
		Status:        "analysis",
		Phase:         "analysis",
		BudgetMinutes: budget,
		StartedAt:     sessionStart,
	}
	if err := r.db.Sessions.Create(ctx, session); err != nil {
		return fmt.Errorf("create session: %w", err)
	}

	r.postProgress(ctx, fmt.Sprintf("🌙 **Nightly session started** for **%s** (budget: %d min)", repoName, budget))

	finishSession := func(status string) {
		now := time.Now()
		session.CompletedAt = &now
		session.Status = status
		r.db.Sessions.Update(ctx, session)

		elapsed := time.Since(sessionStart).Round(time.Second)
		r.postProgress(ctx, fmt.Sprintf(
			"🏁 **Nightly session %s** for **%s** — %d/%d tasks completed, %d failed (%s elapsed)",
			status, repoName, session.TasksCompleted, session.TasksPlanned, session.TasksFailed, elapsed))
	}

	r.wtLock.RLock(repoName)
	specs, err := r.batcher.AnalyzeAll(ctx, repoName, diffs, repoConfig.FocusAreas, repoConfig.MaxTasksPerRun)
	r.wtLock.RUnlock(repoName)
	if err != nil {
		finishSession("failed")
		return fmt.Errorf("LLM analysis: %w", err)
	}

	// Sort: priority ascending, then complexity ascending (easy wins first).
	sort.Slice(specs, func(i, j int) bool {
		if specs[i].Priority != specs[j].Priority {
			return specs[i].Priority < specs[j].Priority
		}
		return complexityOrder(specs[i].Complexity) < complexityOrder(specs[j].Complexity)
	})

	slog.Info("nightly: analysis complete", "repo", repoName, "tasks", len(specs))
	r.postProgress(ctx, fmt.Sprintf("📋 **Analysis complete** — %d tasks identified for **%s**", len(specs), repoName))

	if len(specs) == 0 {
		if currentSHA != "" {
			r.db.SyncRuns.SetRepoSyncSHA(ctx, repoName, currentSHA)
		}
		finishSession("complete")
		return nil
	}

	// Save task records.
	pipelineRunID := newID()
	var tasks []*store.Task
	for _, spec := range specs {
		execType := AssignExecutor(spec)
		branch := BranchName(TaskTypeToPRType(spec.Type), repoName, spec.Title)

		task := &store.Task{
			ID:            newID(),
			Repo:          repoName,
			PipelineRunID: pipelineRunID,
			TaskType:      spec.Type,
			Complexity:    spec.Complexity,
			Title:         spec.Title,
			Description:   spec.Description,
			AffectedFiles: spec.AffectedFiles,
			Acceptance:    spec.AcceptanceCriteria,
			Branch:        branch,
			Executor:      execType,
			Status:        "pending",
			CreatedAt:     time.Now(),
		}
		if err := r.db.Tasks.Create(ctx, *task); err != nil {
			slog.Error("nightly: create task record failed", "err", err)
			continue
		}
		r.db.Tasks.SetSessionID(ctx, task.ID, session.ID)
		tasks = append(tasks, task)
	}

	session.TasksPlanned = len(tasks)
	session.Phase = "planning"
	session.Status = "planning"
	r.db.Sessions.Update(ctx, session)

	// ========================================================================
	// PHASE 2: PLANNING — generate implementation plans via LLM Role H
	// ========================================================================

	r.postProgress(ctx, fmt.Sprintf("📐 **Planning phase** — generating implementation plans for %d tasks", len(tasks)))

	for i, task := range tasks {
		if ctx.Err() != nil {
			slog.Info("nightly: stopped during planning phase")
			finishSession("stopped")
			return nil
		}

		elapsed := time.Since(sessionStart)
		if int(elapsed.Minutes()) >= budget {
			slog.Info("nightly: budget exhausted during planning", "elapsed_min", int(elapsed.Minutes()))
			break
		}

		// Read affected file contents for the planner.
		fileContents := make(map[string]string)
		for _, af := range specs[i].AffectedFiles {
			content, err := r.wt.ReadForgejoFile(ctx, repoName, af)
			if err == nil {
				fileContents[af] = string(content)
			}
		}

		plan, err := r.batcher.Client().PlanTask(ctx, types.PlanOpts{
			Repo:         repoName,
			TaskSpec:     specs[i],
			FileContents: fileContents,
		})
		if err != nil {
			slog.Warn("nightly: planning failed for task", "task", task.Title, "err", err)
			continue
		}

		// Format the plan into a human-readable string for the executor template.
		planText := formatPlan(plan)
		specs[i].ImplementationPlan = planText
		r.db.Tasks.SetImplementationPlan(ctx, task.ID, planText)

		slog.Info("nightly: plan generated", "task", task.Title, "steps", len(plan.Steps))
	}

	// ========================================================================
	// PHASE 3: IMPLEMENTATION + VERIFICATION — [AI_ASSISTANT] Code executes plans
	// ========================================================================

	session.Phase = "implementing"
	session.Status = "implementing"
	r.db.Sessions.Update(ctx, session)

	r.postProgress(ctx, fmt.Sprintf("🔨 **Implementation phase** — executing %d tasks for **%s**", len(tasks), repoName))

	for i, task := range tasks {
		if ctx.Err() != nil {
			slog.Info("nightly: stopped during implementation phase")
			finishSession("stopped")
			return nil
		}

		execType := AssignExecutor(specs[i])
		if execType != "claude_code" {
			slog.Info("nightly: skipping non-[AI_ASSISTANT]-Code task", "type", specs[i].Type, "title", specs[i].Title)
			continue
		}

		r.postProgress(ctx, fmt.Sprintf("▶️ Task %d/%d: **%s** [%s/%s]",
			i+1, len(tasks), task.Title, specs[i].Type, specs[i].Complexity))

		err := r.executeTask(ctx, repoName, task, specs[i])
		if err != nil {
			session.TasksFailed++
			slog.Error("nightly task failed", "repo", repoName, "task", task.Title, "err", err)
		} else {
			session.TasksCompleted++
		}
		r.db.Sessions.Update(ctx, session)
	}

	// ---- Post-task housekeeping ----

	if currentSHA != "" {
		r.db.SyncRuns.SetRepoSyncSHA(ctx, repoName, currentSHA)
	}

	r.db.Actions.Log(ctx, "mode1_nightly_complete", repoName, pipelineRunID,
		fmt.Sprintf(`{"tasks":%d,"completed":%d,"failed":%d}`,
			session.TasksPlanned, session.TasksCompleted, session.TasksFailed))

	// Doc/changelog updates are now included in each task's PR by the [AI_ASSISTANT]
	// Code template — no separate doc-gen or changelog pass needed.

	finishSession("complete")
	slog.Info("nightly pipeline complete", "repo", repoName,
		"completed", session.TasksCompleted, "failed", session.TasksFailed)
	return nil
}

// executeTask creates a branch, invokes [AI_ASSISTANT] Code with the plan, and handles the result.
func (r *NightlyRunner) executeTask(ctx context.Context, repoName string, task *store.Task, spec types.TaskSpec) error {
	branch := task.Branch

	// Acquire write lock and invoke [AI_ASSISTANT] Code.
	r.wtLock.Lock(repoName)
	defer r.wtLock.Unlock(repoName)

	// Create branch on Forgejo.
	headSHA, err := r.forge.GetHeadSHA(ctx, repoName, "main")
	if err != nil {
		r.db.Tasks.SetStatus(ctx, task.ID, "failed")
		return fmt.Errorf("get head SHA: %w", err)
	}
	if err := r.forge.CreateBranch(ctx, repoName, branch, headSHA); err != nil {
		r.db.Tasks.SetStatus(ctx, task.ID, "failed")
		return fmt.Errorf("create branch %s: %w", branch, err)
	}

	r.db.Tasks.SetStatus(ctx, task.ID, "running")

	// Set a complexity-based timeout for [AI_ASSISTANT] Code.
	timeout := taskTimeoutMinutes(spec.Complexity)
	taskCtx, taskCancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Minute)
	defer taskCancel()

	spec.ID = task.ID
	result, err := r.executor.Execute(taskCtx, spec, branch, repoName)
	if err != nil || !result.Success {
		r.db.Tasks.SetStatus(ctx, task.ID, "failed")
		r.db.Tasks.SetVerification(ctx, task.ID, "skipped", "")
		errMsg := "unknown error"
		if err != nil {
			errMsg = err.Error()
		} else if result != nil {
			errMsg = result.Error
		}
		return fmt.Errorf("[AI_ASSISTANT] code task %s: %s", task.ID, errMsg)
	}

	r.db.Tasks.SetStatus(ctx, task.ID, "complete")
	r.db.Tasks.SetVerification(ctx, task.ID, "passed", "")

	// Create the PR directly instead of waiting for a webhook roundtrip.
	if err := r.openPR(ctx, repoName, task, spec); err != nil {
		slog.Error("nightly: PR creation failed", "repo", repoName, "branch", branch, "err", err)
		// Task succeeded even if PR creation fails — the branch exists on Forgejo.
	}

	return nil
}

// openPR creates a Forgejo PR and posts the notification to the sentinel-prs Discord channel.
func (r *NightlyRunner) openPR(ctx context.Context, repoName string, task *store.Task, spec types.TaskSpec) error {
	prType := TaskTypeToPRType(spec.Type)
	prTitle := PRTitleFor(spec)
	tier := PriorityTier(prType, r.cfg.PR.HighPriorityTypes)

	// Build a structured PR body.
	var body strings.Builder
	body.WriteString("## Summary\n")
	body.WriteString(task.Description)
	body.WriteString("\n")
	if len(task.AffectedFiles) > 0 {
		body.WriteString("\n## Affected Files\n")
		for _, f := range task.AffectedFiles {
			body.WriteString("- `" + f + "`\n")
		}
	}
	if len(task.Acceptance) > 0 {
		body.WriteString("\n## Acceptance Criteria\n")
		for _, c := range task.Acceptance {
			body.WriteString("- " + c + "\n")
		}
	}
	body.WriteString(fmt.Sprintf("\n---\n*Type: `%s` | Complexity: `%s` | Executor: `%s`*\n",
		task.TaskType, task.Complexity, task.Executor))

	prBody := body.String()

	prNumber, prURL, err := r.forge.CreatePR(ctx, types.OpenPROptions{
		Repo:         repoName,
		Branch:       task.Branch,
		BaseBranch:   "main",
		Title:        prTitle,
		Body:         prBody,
		PRType:       prType,
		PriorityTier: tier,
	})
	if err != nil {
		return fmt.Errorf("create PR for %s: %w", task.Branch, err)
	}

	prChID := r.cfg.Discord.PRChannelID
	if prChID == "" {
		prChID = r.cfg.Discord.ActionsChannelID
	}

	pr := types.SentinelPR{
		ID:               newID(),
		Repo:             repoName,
		PRNumber:         prNumber,
		PRUrl:            prURL,
		Branch:           task.Branch,
		BaseBranch:       "main",
		Title:            prTitle,
		PRType:           prType,
		PriorityTier:     tier,
		Status:           types.PRStatusOpen,
		OpenedAt:         time.Now(),
		TaskID:           task.ID,
		DiscordChannelID: prChID,
	}

	msgID, err := r.notifier.PostPRNotification(ctx, pr, prBody)
	if err != nil {
		slog.Error("nightly: post PR notification failed", "repo", repoName, "pr", prNumber, "err", err)
	}
	pr.DiscordMessageID = msgID

	if err := r.db.PRs.Create(ctx, pr); err != nil {
		return fmt.Errorf("save PR record: %w", err)
	}

	r.db.Tasks.SetPRNumber(ctx, task.ID, prNumber)

	slog.Info("nightly: PR opened", "repo", repoName, "branch", task.Branch, "pr", prNumber)
	return nil
}

// postProgress sends a status message to both Discord and SSE clients.
func (r *NightlyRunner) postProgress(ctx context.Context, msg string) {
	// SSE event for dashboard.
	r.publishEvent("progress", map[string]string{
		"message":   msg,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	})

	if r.discord == nil {
		return
	}
	channelID := r.cfg.Discord.LogsChannelID
	if channelID == "" {
		return
	}
	if err := r.discord.PostChannelMessage(ctx, channelID, msg); err != nil {
		slog.Warn("nightly: discord progress post failed", "err", err)
	}
}

// filterByBudget removes tasks that won't fit within the total session budget.
func (r *NightlyRunner) filterByBudget(specs []types.TaskSpec, budgetMinutes int) []types.TaskSpec {
	var out []types.TaskSpec
	totalEstimate := 0
	for _, s := range specs {
		est := taskBudgetMinutes(s.Complexity)
		if totalEstimate+est > budgetMinutes {
			continue
		}
		totalEstimate += est
		out = append(out, s)
	}
	return out
}

// formatPlan converts a TaskPlan into a readable string for the executor template.
func formatPlan(plan *types.TaskPlan) string {
	var sb strings.Builder
	if plan.Approach != "" {
		sb.WriteString("### Approach\n")
		sb.WriteString(plan.Approach)
		sb.WriteString("\n\n")
	}
	if len(plan.Steps) > 0 {
		sb.WriteString("### Steps\n")
		for i, step := range plan.Steps {
			sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, step))
		}
		sb.WriteString("\n")
	}
	if len(plan.FilesModified) > 0 {
		sb.WriteString("### Files to modify\n")
		for _, f := range plan.FilesModified {
			sb.WriteString("- " + f + "\n")
		}
		sb.WriteString("\n")
	}
	if plan.Risks != "" {
		sb.WriteString("### Risks\n")
		sb.WriteString(plan.Risks)
		sb.WriteString("\n")
	}
	return sb.String()
}

func taskBudgetMinutes(complexity string) int {
	if m, ok := complexityBudget[complexity]; ok {
		return m
	}
	return 30 // default
}

func taskTimeoutMinutes(complexity string) int {
	if m, ok := complexityTimeout[complexity]; ok {
		return m
	}
	return 30 // default
}

func complexityOrder(c string) int {
	switch c {
	case "trivial":
		return 0
	case "small":
		return 1
	case "medium":
		return 2
	case "large":
		return 3
	default:
		return 4
	}
}

// getFileDiffs returns per-file diffs between lastSHA and current HEAD.
// If lastSHA is empty, diffs all files against the empty tree.
func (r *NightlyRunner) getFileDiffs(_ context.Context, repoName, lastSHA string) ([]types.FileDiff, string, error) {
	dir := r.wt.ForgejoDir(repoName)

	repo, err := gogit.PlainOpen(dir)
	if err != nil {
		return nil, "", err
	}

	head, err := repo.Head()
	if err != nil {
		return nil, "", err
	}
	currentSHA := head.Hash().String()

	if lastSHA == currentSHA {
		return nil, currentSHA, nil
	}

	headCommit, err := repo.CommitObject(head.Hash())
	if err != nil {
		return nil, "", err
	}
	headTree, err := headCommit.Tree()
	if err != nil {
		return nil, "", err
	}

	var baseTree *object.Tree
	if lastSHA != "" {
		baseHash := plumbing.NewHash(lastSHA)
		baseCommit, err := repo.CommitObject(baseHash)
		if err == nil {
			baseTree, _ = baseCommit.Tree()
		}
	}

	var diffs []types.FileDiff

	if baseTree == nil {
		// First run: walk all files in the tree and treat every line as added.
		err = headTree.Files().ForEach(func(f *object.File) error {
			content, ferr := f.Contents()
			if ferr != nil {
				return nil // skip unreadable files
			}
			lines := 0
			for _, c := range content {
				if c == '\n' {
					lines++
				}
			}
			diffs = append(diffs, types.FileDiff{
				Filename:     f.Name,
				Diff:         content,
				LinesAdded:   lines,
				LinesRemoved: 0,
				EstTokens:    len(content) / 4,
			})
			return nil
		})
		if err != nil {
			return nil, currentSHA, fmt.Errorf("walk head tree: %w", err)
		}
		return diffs, currentSHA, nil
	}

	changes, err := headTree.Diff(baseTree)
	if err != nil {
		return nil, currentSHA, fmt.Errorf("diff trees: %w", err)
	}

	for _, change := range changes {
		patch, err := change.Patch()
		if err != nil {
			continue
		}
		diffStr := patch.String()
		name := change.To.Name
		if name == "" {
			name = change.From.Name
		}

		stats := patch.Stats()
		var added, removed int
		for _, s := range stats {
			added += s.Addition
			removed += s.Deletion
		}

		diffs = append(diffs, types.FileDiff{
			Filename:     name,
			Diff:         diffStr,
			LinesAdded:   added,
			LinesRemoved: removed,
			EstTokens:    len(diffStr) / 4,
		})
	}

	return diffs, currentSHA, nil
}

func (r *NightlyRunner) repoConfig(name string) *config.RepoConfig {
	for i := range r.cfg.Repos {
		if r.cfg.Repos[i].Name == name {
			return &r.cfg.Repos[i]
		}
	}
	return nil
}

func newID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}
