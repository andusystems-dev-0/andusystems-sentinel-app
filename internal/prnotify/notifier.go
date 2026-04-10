// Package prnotify manages PR notification embeds, ✅/❌/💬 reaction handlers,
// and Forgejo↔Discord state synchronisation.
//
// Only this package calls the Forgejo merge API with the operator token.
// Must-NOT call Ollama, write worktree files.
package prnotify

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/andusystems/sentinel/internal/config"
	"github.com/andusystems/sentinel/internal/discord"
	"github.com/andusystems/sentinel/internal/types"
)

// Notifier implements types.PRNotifier.
type Notifier struct {
	bot     *discord.Bot
	forge   types.ForgejoProvider
	prStore types.SentinelPRStore
	actions types.ActionLogger
	mention *MentionTracker
	cfg     *config.Config
}

// NewNotifier creates a PRNotifier.
func NewNotifier(
	bot *discord.Bot,
	forge types.ForgejoProvider,
	prStore types.SentinelPRStore,
	actions types.ActionLogger,
	mention *MentionTracker,
	cfg *config.Config,
) *Notifier {
	return &Notifier{
		bot:     bot,
		forge:   forge,
		prStore: prStore,
		actions: actions,
		mention: mention,
		cfg:     cfg,
	}
}

func (n *Notifier) prChannelID() string {
	if n.cfg.Discord.PRChannelID != "" {
		return n.cfg.Discord.PRChannelID
	}
	return n.cfg.Discord.ActionsChannelID
}

// PostPRNotification posts a PR embed to the Discord PR channel and seeds reactions.
// Returns the Discord message ID.
func (n *Notifier) PostPRNotification(ctx context.Context, pr types.SentinelPR, summary string) (string, error) {
	embed := discord.BuildPREmbed(pr, summary, n.cfg.PR)

	session := n.bot.Session()
	msg, err := session.ChannelMessageSendEmbed(n.prChannelID(), embed)
	if err != nil {
		return "", fmt.Errorf("post PR notification embed: %w", err)
	}

	// Seed reactions.
	if err := n.bot.SeedPRReactions(ctx, n.prChannelID(), msg.ID); err != nil {
		slog.Warn("seed PR reactions failed", "messageID", msg.ID, "err", err)
	}

	// @here mention for vulnerability PRs (with per-repo cooldown).
	if n.cfg.PR.MentionOnSecurity && pr.PRType == "vulnerability" {
		if n.mention.ShouldMention(ctx, pr.Repo) {
			content := fmt.Sprintf("@here 🚨 New vulnerability PR in **%s** — please review.", pr.Repo)
			n.bot.PostChannelMessage(ctx, n.prChannelID(), content)
			n.mention.RecordMention(ctx, pr.Repo)
		}
	}

	n.actions.Log(ctx, "pr_notification_posted", pr.Repo, pr.ID,
		fmt.Sprintf(`{"pr_number":%d,"discord_message_id":"%s"}`, pr.PRNumber, msg.ID))

	return msg.ID, nil
}

// HandleApprove processes ✅ on a PR message: merges via operator token.
func (n *Notifier) HandleApprove(ctx context.Context, pr *types.SentinelPR, userID string) error {
	if pr.Status != types.PRStatusOpen {
		return nil // idempotent
	}

	strategy := n.cfg.PR.MergeStrategy
	for _, repo := range n.cfg.Repos {
		if repo.Name == pr.Repo && repo.MergeStrategy != "" {
			strategy = repo.MergeStrategy
		}
	}

	if err := n.forge.MergePR(ctx, pr.Repo, pr.PRNumber, strategy, n.cfg.Forgejo.OperatorToken); err != nil {
		slog.Error("merge PR failed", "pr", pr.PRNumber, "repo", pr.Repo, "err", err)
		n.bot.PostChannelMessage(ctx, n.prChannelID(),
			fmt.Sprintf("⚠️ Failed to merge PR #%d in `%s` — token may be expired. Retry or merge in Forgejo UI.", pr.PRNumber, pr.Repo))
		return err
	}

	if err := n.prStore.MarkMerged(ctx, pr.ID, userID); err != nil {
		return err
	}

	ts := time.Now().Format("15:04 MST")
	n.bot.EditPRFooter(ctx, pr.DiscordChannelID, pr.DiscordMessageID,
		fmt.Sprintf("✅ Merged by <@%s> · %s", userID, ts))
	n.bot.PostChannelMessage(ctx, n.prChannelID(),
		fmt.Sprintf("✅ **PR #%d** merged in **%s** — `%s` → `main` · <@%s> · %s\n*GitHub mirror will sync shortly.*",
			pr.PRNumber, pr.Repo, pr.Branch, userID, ts))

	n.actions.Log(ctx, "pr_merged", pr.Repo, pr.ID,
		fmt.Sprintf(`{"pr_number":%d,"merged_by":"%s"}`, pr.PRNumber, userID))

	// Refresh the operator's local working clone for docs PRs so that the
	// next push from /home/admin/andusystems/<repo> cannot accidentally
	// overwrite the documentation we just merged.
	if isDocsPR(pr) {
		pullLocalCheckout(ctx, n.cfg.Sentinel.LocalCheckoutBase, pr.Repo)
	}
	return nil
}

// isDocsPR returns true if pr was opened by the doc-gen pipeline.
// Identified by either the explicit PR type or the sentinel/docs/ branch prefix.
func isDocsPR(pr *types.SentinelPR) bool {
	if pr == nil {
		return false
	}
	if pr.PRType == "docs" {
		return true
	}
	return strings.HasPrefix(pr.Branch, "sentinel/docs/")
}

// HandleClose processes ❌ on a PR message: closes via sentinel token.
func (n *Notifier) HandleClose(ctx context.Context, pr *types.SentinelPR, userID string) error {
	if pr.Status != types.PRStatusOpen {
		return nil
	}

	if err := n.forge.ClosePR(ctx, pr.Repo, pr.PRNumber); err != nil {
		return fmt.Errorf("close PR %s#%d: %w", pr.Repo, pr.PRNumber, err)
	}

	if err := n.prStore.MarkClosed(ctx, pr.ID, userID); err != nil {
		return err
	}

	n.bot.EditPRFooter(ctx, pr.DiscordChannelID, pr.DiscordMessageID,
		fmt.Sprintf("❌ Closed by <@%s> · %s", userID, time.Now().Format("15:04 MST")))

	n.actions.Log(ctx, "pr_closed", pr.Repo, pr.ID,
		fmt.Sprintf(`{"pr_number":%d,"closed_by":"%s"}`, pr.PRNumber, userID))
	return nil
}

// HandleDiscuss processes 💬 on a PR message: opens a discussion thread.
func (n *Notifier) HandleDiscuss(ctx context.Context, pr *types.SentinelPR) error {
	if pr.DiscordThreadID != "" {
		return nil // thread already open
	}

	threadID, err := n.bot.OpenThread(ctx, pr.DiscordChannelID, pr.DiscordMessageID,
		fmt.Sprintf("Discussion: %s", pr.Title))
	if err != nil {
		return err
	}

	if err := n.prStore.SetThread(ctx, pr.ID, threadID); err != nil {
		return err
	}

	welcome := fmt.Sprintf(
		"**PR Discussion**\n- Repo: `%s`\n- Branch: `%s` → `%s`\n- Forgejo: %s\n\nAsk a question and I'll answer using the PR diff as context.",
		pr.Repo, pr.Branch, pr.BaseBranch, pr.PRUrl,
	)
	return n.bot.PostInThread(ctx, threadID, welcome)
}

// HandleForgejoResolution handles merge or close events arriving via Forgejo webhook.
// This is the Forgejo→Discord sync direction.
func (n *Notifier) HandleForgejoResolution(ctx context.Context, repo string, prNumber int, merged bool) error {
	pr, err := n.prStore.GetByPRNumber(ctx, repo, prNumber)
	if err != nil || pr == nil {
		return err
	}

	// Idempotency check.
	if pr.Status != types.PRStatusOpen {
		slog.Info("PR already in terminal state (Forgejo webhook)", "pr", pr.ID, "status", pr.Status)
		return nil
	}

	ts := time.Now().Format("15:04 MST")

	if merged {
		n.prStore.MarkMerged(ctx, pr.ID, "forgejo-ui")
		n.bot.EditPRFooter(ctx, pr.DiscordChannelID, pr.DiscordMessageID,
			fmt.Sprintf("✅ Merged in Forgejo · %s", ts))
		n.bot.PostChannelMessage(ctx, n.prChannelID(),
			fmt.Sprintf("✅ **PR #%d** merged in **%s** — `%s` → `main` · via Forgejo UI · %s\n*GitHub mirror will sync shortly.*",
				prNumber, repo, pr.Branch, ts))
		n.actions.Log(ctx, "forgejo_sync_merged", repo, pr.ID, fmt.Sprintf(`{"pr_number":%d}`, prNumber))
		if isDocsPR(pr) {
			pullLocalCheckout(ctx, n.cfg.Sentinel.LocalCheckoutBase, repo)
		}
	} else {
		n.prStore.MarkClosed(ctx, pr.ID, "forgejo-ui")
		n.bot.EditPRFooter(ctx, pr.DiscordChannelID, pr.DiscordMessageID,
			fmt.Sprintf("❌ Closed in Forgejo · %s", ts))
		n.bot.PostChannelMessage(ctx, n.prChannelID(),
			fmt.Sprintf("❌ **PR #%d** closed in **%s** — `%s` · via Forgejo UI · %s",
				prNumber, repo, pr.Branch, ts))
		n.actions.Log(ctx, "forgejo_sync_closed", repo, pr.ID, fmt.Sprintf(`{"pr_number":%d}`, prNumber))
	}

	return nil
}
