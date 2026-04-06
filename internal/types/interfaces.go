// Package types defines all shared interfaces used across sentinel packages.
package types

import "context"

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
	RemoveFromGitHubStaging(ctx context.Context, repo, filename string) error
	ReadGitHubStaging(ctx context.Context, repo, filename string) ([]byte, error)
	// ResolveTag — caller MUST hold FileMutexRegistry lock for (repo, filename)
	ResolveTag(ctx context.Context, repo, filename string,
		tokenIndex, resolvedCount int, finalValue string) error
	// PushStagingFile — caller MUST hold FileMutexRegistry lock for (repo, filename)
	PushStagingFile(ctx context.Context, repo, filename, commitMsg string) (commitSHA string, err error)
	PushAllStaging(ctx context.Context, repo, commitMsg string) (commitSHA string, err error)
	// PushAllStagingInitial force-pushes a single orphan commit to remote main.
	// Used by Mode 4 initial migration for a clean single-commit history.
	PushAllStagingInitial(ctx context.Context, repo, commitMsg string) (commitSHA string, err error)
	// ResetGitHubStaging wipes the local GitHub staging directory and its git history.
	ResetGitHubStaging(ctx context.Context, repo string) error
	SentinelTag(category string) string
}

// WebhookQueue decouples HTTP ACK from async event processing.
type WebhookQueue interface {
	Enqueue(event ForgejoEvent) error // Returns error if queue full (caller returns HTTP 429)
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
	SeedCommandReactions(ctx context.Context, channelID, messageID string) error
	EditFindingFooter(ctx context.Context, channelID, messageID, footer string) error
	EditPRFooter(ctx context.Context, channelID, messageID, footer string) error
	PostChannelMessage(ctx context.Context, channelID, content string) error
	PostChannelMessageID(ctx context.Context, channelID, content string) (string, error)
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
	GetHeadSHA(ctx context.Context, repo, branch string) (string, error)
}

// GitHubProvider wraps the GitHub API for the staging mirror.
type GitHubProvider interface {
	EnsureRepo(ctx context.Context, repoPath, description string) error
	PushFile(ctx context.Context, repoPath, filename, commitMsg string, content []byte) error
	PushFiles(ctx context.Context, repoPath, commitMsg string, files map[string][]byte) error
}

// ActionLogger records sentinel-originated actions for audit.
type ActionLogger interface {
	Log(ctx context.Context, actionType, repo, entityID, detail string) error
}

// SyncRunStore manages sync run records.
type SyncRunStore interface {
	Create(ctx context.Context, run SyncRun) error
	Update(ctx context.Context, run SyncRun) error
	GetByID(ctx context.Context, id string) (*SyncRun, error)
}
