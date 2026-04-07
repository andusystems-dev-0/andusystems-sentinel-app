package discord

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

// CommandDispatcher defines a function to call for a given command.
type CommandDispatcher struct {
	handlers map[string]CommandHandler
}

// CommandHandler processes a parsed sentinel command.
type CommandHandler func(ctx context.Context, bot *Bot, args []string, userID, channelID string) error

// NewCommandDispatcher creates a dispatcher with registered commands.
func NewCommandDispatcher(handlers map[string]CommandHandler) *CommandDispatcher {
	return &CommandDispatcher{handlers: handlers}
}

// handleCommand parses and dispatches a /sentinel command from the command channel.
// Commands take the form: /sentinel <subcommand> [args...]
func handleCommand(ctx context.Context, bot *Bot, message, userID, channelID string) {
	if !strings.HasPrefix(message, "/sentinel") {
		return
	}

	parts := strings.Fields(message)
	if len(parts) < 2 {
		bot.PostChannelMessage(ctx, channelID, usage())
		return
	}

	if !bot.IsOperator(userID) {
		bot.PostChannelMessage(ctx, channelID, "⛔ Only operators can use sentinel commands.")
		return
	}

	subcommand := parts[1]
	args := parts[2:]

	switch subcommand {
	case "status":
		bot.PostChannelMessage(ctx, channelID, "✅ Sentinel is running.")
	case "stop":
		slog.Info("stop command received", "user", userID)
		if bot.nightlyRunner != nil {
			bot.nightlyRunner.Stop()
			bot.PostChannelMessage(ctx, channelID, "🛑 Stop signal sent — current task will finish, no new tasks will start.")
		} else {
			bot.PostChannelMessage(ctx, channelID, "⚠️ No nightly runner available.")
		}
	case "migrate":
		if len(args) == 0 {
			bot.PostChannelMessage(ctx, channelID, "Usage: `/sentinel migrate <repo> [--force]`")
			return
		}
		slog.Info("migrate command received", "repo", args[0], "user", userID)
		bot.PostChannelMessage(ctx, channelID, fmt.Sprintf("🔄 Migration for `%s` queued. Watch this channel for updates.", args[0]))
	case "sync":
		if len(args) == 0 {
			bot.PostChannelMessage(ctx, channelID, "Usage: `/sentinel sync <repo>`")
			return
		}
		slog.Info("sync command received", "repo", args[0], "user", userID)
		bot.PostChannelMessage(ctx, channelID, fmt.Sprintf("🔄 Sync for `%s` queued.", args[0]))
	case "run":
		slog.Info("manual nightly run requested", "user", userID, "args", args)
		if bot.nightlyRunner == nil {
			bot.PostChannelMessage(ctx, channelID, "⚠️ No nightly runner available.")
			return
		}
		// /sentinel run              → incremental run on all repos
		// /sentinel run <repo>       → incremental run on one repo
		// /sentinel run <repo> full  → full scan on one repo
		if len(args) == 0 {
			bot.PostChannelMessage(ctx, channelID, "🌙 Incremental nightly run started for all repos.")
			go func() {
				if err := bot.nightlyRunner.RunAll(ctx); err != nil {
					slog.Error("manual nightly run failed", "err", err)
				}
			}()
		} else {
			repo := args[0]
			full := len(args) > 1 && args[1] == "full"
			if full {
				bot.PostChannelMessage(ctx, channelID, fmt.Sprintf("🌙 Full nightly scan started for **%s**.", repo))
				go func() {
					if err := bot.nightlyRunner.RunFull(ctx, repo); err != nil {
						slog.Error("manual nightly run failed", "repo", repo, "err", err)
					}
				}()
			} else {
				bot.PostChannelMessage(ctx, channelID, fmt.Sprintf("🌙 Incremental nightly run started for **%s**.", repo))
				go func() {
					if err := bot.nightlyRunner.Run(ctx, repo); err != nil {
						slog.Error("manual nightly run failed", "repo", repo, "err", err)
					}
				}()
			}
		}
	case "help":
		bot.PostChannelMessage(ctx, channelID, usage())
	default:
		bot.PostChannelMessage(ctx, channelID, fmt.Sprintf("Unknown command `%s`. %s", subcommand, usage()))
	}
}

func usage() string {
	return "**Sentinel Commands**\n" +
		"```\n" +
		"/sentinel status              — Check sentinel status\n" +
		"/sentinel stop                — Stop active nightly session\n" +
		"/sentinel migrate <repo>      — Run Mode 4 initial migration\n" +
		"/sentinel migrate <repo> --force  — Force re-migration\n" +
		"/sentinel sync <repo>         — Run Mode 3 incremental sync\n" +
		"/sentinel run                 — Incremental nightly on all repos\n" +
		"/sentinel run <repo>          — Incremental nightly on one repo\n" +
		"/sentinel run <repo> full     — Full scan nightly on one repo\n" +
		"/sentinel help                — Show this help\n" +
		"```"
}
