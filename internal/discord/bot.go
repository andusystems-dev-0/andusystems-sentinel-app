package discord

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/bwmarrin/discordgo"

	"github.com/andusystems/sentinel/internal/config"
	"github.com/andusystems/sentinel/internal/store"
	"github.com/andusystems/sentinel/internal/types"
)

// NightlyController allows the bot to trigger and stop nightly sessions.
type NightlyController interface {
	Stop()
	RunAll(ctx context.Context) error
	RunAllFull(ctx context.Context) error
	Run(ctx context.Context, repo string) error
	RunFull(ctx context.Context, repo string) error
}

// Bot implements types.DiscordBot. It owns the Discord gateway session.
type Bot struct {
	session         *discordgo.Session
	cfg             *config.Config
	findingHandlers map[string]types.ReactionHandler
	prHandlers      map[string]types.ReactionHandler
	confirmations   *store.ConfirmationStore
	nightlyRunner   NightlyController
}

// SetConfirmationStore injects the confirmation store so the bot can resolve
// ✅/❌ reactions in the command channel.
func (b *Bot) SetConfirmationStore(s *store.ConfirmationStore) {
	b.confirmations = s
}

// SetNightlyRunner injects the nightly runner for run/stop command support.
func (b *Bot) SetNightlyRunner(nr NightlyController) {
	b.nightlyRunner = nr
}

// NewBot creates a Discord bot but does not connect yet. Call Start to connect.
func NewBot(cfg *config.Config) (*Bot, error) {
	session, err := discordgo.New("Bot " + cfg.Discord.BotToken)
	if err != nil {
		return nil, fmt.Errorf("create discord session: %w", err)
	}

	// Enable necessary intents: guild messages, message reactions, guild members.
	session.Identify.Intents = discordgo.IntentsGuildMessages |
		discordgo.IntentsGuildMessageReactions |
		discordgo.IntentsGuildMembers

	return &Bot{
		session:         session,
		cfg:             cfg,
		findingHandlers: make(map[string]types.ReactionHandler),
		prHandlers:      make(map[string]types.ReactionHandler),
	}, nil
}

// RegisterFindingHandler registers a reaction handler for the given emoji on finding messages.
func (b *Bot) RegisterFindingHandler(h types.ReactionHandler) {
	b.findingHandlers[h.Emoji()] = h
}

// RegisterPRHandler registers a reaction handler for the given emoji on PR messages.
func (b *Bot) RegisterPRHandler(h types.ReactionHandler) {
	b.prHandlers[h.Emoji()] = h
}

// Start connects to Discord and begins processing events.
func (b *Bot) Start(_ context.Context) error {
	b.session.AddHandler(b.onReactionAdd)
	b.session.AddHandler(b.onMessageCreate)

	if err := b.session.Open(); err != nil {
		return fmt.Errorf("open discord session: %w", err)
	}
	slog.Info("Discord bot connected", "guild", b.cfg.Discord.GuildID)
	return nil
}

// Stop disconnects from Discord gracefully.
func (b *Bot) Stop() error {
	return b.session.Close()
}

// PostFinding posts a sanitization finding embed to the logs channel.
func (b *Bot) PostFinding(_ context.Context, r types.PendingResolution, f types.SanitizationFinding) (string, error) {
	embed := BuildFindingEmbed(r, f)
	msg, err := b.session.ChannelMessageSendEmbed(b.cfg.Discord.LogsChannelID, embed)
	if err != nil {
		return "", fmt.Errorf("post finding embed: %w", err)
	}
	return msg.ID, nil
}

// SeedFindingReactions adds the four reaction emojis to a finding message.
func (b *Bot) SeedFindingReactions(_ context.Context, channelID, messageID string) error {
	for _, emoji := range []string{"✅", "❌", "🔍", "✏️"} {
		if err := b.session.MessageReactionAdd(channelID, messageID, emoji); err != nil {
			return fmt.Errorf("seed finding reaction %s: %w", emoji, err)
		}
	}
	return nil
}

// SeedPRReactions adds the three PR reaction emojis.
func (b *Bot) SeedPRReactions(_ context.Context, channelID, messageID string) error {
	for _, emoji := range []string{"✅", "❌", "💬"} {
		if err := b.session.MessageReactionAdd(channelID, messageID, emoji); err != nil {
			return fmt.Errorf("seed PR reaction %s: %w", emoji, err)
		}
	}
	return nil
}

// EditFindingFooter updates the footer text on an existing embed message.
func (b *Bot) EditFindingFooter(_ context.Context, channelID, messageID, footer string) error {
	return b.editEmbedFooter(channelID, messageID, footer)
}

// EditPRFooter updates the footer text on an existing PR embed message.
func (b *Bot) EditPRFooter(_ context.Context, channelID, messageID, footer string) error {
	return b.editEmbedFooter(channelID, messageID, footer)
}

// SeedCommandReactions adds ✅ and ❌ reactions to a command channel message.
func (b *Bot) SeedCommandReactions(_ context.Context, channelID, messageID string) error {
	for _, emoji := range []string{"✅", "❌"} {
		if err := b.session.MessageReactionAdd(channelID, messageID, emoji); err != nil {
			return fmt.Errorf("seed command reaction %s: %w", emoji, err)
		}
	}
	return nil
}

// messageSeparator is appended to every plain-text Discord message to give
// long streams of sentinel output a visual break between entries.
const messageSeparator = "\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

// PostChannelMessage sends a plain text message to a channel.
func (b *Bot) PostChannelMessage(_ context.Context, channelID, content string) error {
	_, err := b.session.ChannelMessageSend(channelID, content+messageSeparator)
	return err
}

// PostChannelMessageID sends a plain text message and returns the message ID.
func (b *Bot) PostChannelMessageID(_ context.Context, channelID, content string) (string, error) {
	msg, err := b.session.ChannelMessageSend(channelID, content+messageSeparator)
	if err != nil {
		return "", err
	}
	return msg.ID, nil
}

// OpenThread opens a Discord thread on a message.
func (b *Bot) OpenThread(_ context.Context, channelID, messageID, name string) (string, error) {
	thread, err := b.session.MessageThreadStartComplex(channelID, messageID, &discordgo.ThreadStart{
		Name:                name,
		AutoArchiveDuration: 1440, // 24 hours
	})
	if err != nil {
		return "", fmt.Errorf("open thread: %w", err)
	}
	return thread.ID, nil
}

// PostInThread sends a message into a thread.
func (b *Bot) PostInThread(_ context.Context, threadID, content string) error {
	_, err := b.session.ChannelMessageSend(threadID, content+messageSeparator)
	return err
}

// PostNightlyDigest posts the nightly digest embed to the PR channel.
func (b *Bot) PostNightlyDigest(_ context.Context, digest types.NightlyDigest) error {
	embed := BuildDigestEmbed(digest, b.cfg.Digest.LowPriorityCollapseThreshold)
	_, err := b.session.ChannelMessageSendEmbed(b.prCh(), embed)
	return err
}

// ---- internal event handlers ------------------------------------------------

// onReactionAdd is called by discordgo for every MessageReactionAdd event.
func (b *Bot) onReactionAdd(s *discordgo.Session, r *discordgo.MessageReactionAdd) {
	// Ignore bot's own reactions.
	if r.UserID == s.State.User.ID {
		return
	}

	ctx := context.Background()
	emoji := r.Emoji.Name

	// Logs channel: finding reactions (✅/❌/🔍/✏️).
	if r.ChannelID == b.cfg.Discord.LogsChannelID {
		if h, ok := b.findingHandlers[emoji]; ok {
			if err := h.Handle(ctx, r.MessageID, r.UserID); err != nil {
				slog.Error("finding reaction handler error", "emoji", emoji, "err", err)
			}
		}
		return
	}

	// PR channel: PR reactions (✅/❌/💬).
	if r.ChannelID == b.prCh() {
		if h, ok := b.prHandlers[emoji]; ok {
			if err := h.Handle(ctx, r.MessageID, r.UserID); err != nil {
				slog.Error("PR reaction handler error", "emoji", emoji, "err", err)
			}
		}
		return
	}

	// Actions channel: confirmation reactions (✅/❌) for migrations.
	if r.ChannelID == b.cfg.Discord.ActionsChannelID {
		if b.confirmations != nil && b.IsOperator(r.UserID) {
			switch emoji {
			case "✅":
				b.confirmations.SetStatusByMessageID(ctx, r.MessageID, "confirmed")
			case "❌":
				b.confirmations.SetStatusByMessageID(ctx, r.MessageID, "rejected")
			}
		}
		return
	}
}

// onMessageCreate handles /sentinel slash commands posted in the command channel.
func (b *Bot) onMessageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {
	if m.Author.ID == s.State.User.ID {
		return
	}
	if m.ChannelID != b.cfg.Discord.ActionsChannelID {
		return
	}
	// Command parsing is handled by commands.go dispatcher.
	handleCommand(context.Background(), b, m.Content, m.Author.ID, m.ChannelID)
}

// ---- helpers ----------------------------------------------------------------

// prCh returns the PR channel ID, falling back to actions channel if not set.
func (b *Bot) prCh() string {
	if b.cfg.Discord.PRChannelID != "" {
		return b.cfg.Discord.PRChannelID
	}
	return b.cfg.Discord.ActionsChannelID
}

func (b *Bot) editEmbedFooter(channelID, messageID, footer string) error {
	msg, err := b.session.ChannelMessage(channelID, messageID)
	if err != nil {
		return fmt.Errorf("fetch message for embed edit: %w", err)
	}
	if len(msg.Embeds) == 0 {
		return fmt.Errorf("message %s has no embeds", messageID)
	}

	embed := msg.Embeds[0]
	embed.Footer = &discordgo.MessageEmbedFooter{Text: footer}

	_, err = b.session.ChannelMessageEditEmbed(channelID, messageID, embed)
	return err
}

// IsOperator returns true if the userID is in the configured operator allowlist.
// Bot accounts are excluded automatically (discordgo sets bot flag on user objects).
func (b *Bot) IsOperator(userID string) bool {
	for _, id := range b.cfg.Discord.OperatorUserIDs {
		if id == userID {
			return true
		}
	}
	return false
}

// Session returns the underlying discordgo session (for use by reaction handlers).
func (b *Bot) Session() *discordgo.Session { return b.session }
