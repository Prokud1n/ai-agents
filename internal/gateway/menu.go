package gateway

import (
	"context"

	"github.com/Prokud1n/ai-agents/internal/providers/telegram"
)

// MenuCommand is an item of the command menu: the command without the slash and its short description.
type MenuCommand struct {
	Command     Command
	Description string
}

// Menu is the command menu of the team group: the client shows it when the trainer types «/». Only the dispatcher (the
// marketer, who answers the team commands) shows it; the other bots have their menus removed, so the commands are not
// listed four times. The gateway module provides an empty Menu (the menus are left as they are); in the application
// teamchat.Module replaces it with fx.Decorate.
type Menu struct {
	Commands []MenuCommand
}

// NewMenu is the empty menu of the gateway module alone.
func NewMenu() Menu {
	return Menu{}
}

// menuScopes are the scopes whose menus are removed from the other bots: the group itself, all groups and the default
// (a menu set in @BotFather), so the group falls back to none of them.
func menuScopes(groupID int64) []*telegram.BotCommandScope {
	return []*telegram.BotCommandScope{
		{Type: telegram.ChatCommandScope, ChatID: groupID},
		{Type: telegram.AllGroupChatsCommandScope},
		{Type: telegram.DefaultCommandScope},
	}
}

// setupMenu sets the menu of the dispatcher in the group and removes the menus of the other bots. It runs once the bot is
// connected; a failure is only logged: the commands work without the menu.
func (g *gateway) setupMenu(ctx context.Context, b *bot) {
	if len(g.menu.Commands) == 0 {
		return
	}

	if b.role == dispatcherRole {
		commands := make([]telegram.BotCommand, 0, len(g.menu.Commands))
		for _, c := range g.menu.Commands {
			commands = append(commands, telegram.BotCommand{Command: string(c.Command), Description: c.Description})
		}

		err := b.client.SetMyCommands(ctx, &telegram.SetMyCommandsParams{
			Commands: commands,
			Scope:    &telegram.BotCommandScope{Type: telegram.ChatCommandScope, ChatID: g.routing.GroupID},
		})
		g.logMenuError(ctx, b, "failed to set the command menu", err)

		return
	}

	for _, scope := range menuScopes(g.routing.GroupID) {
		err := b.client.DeleteMyCommands(ctx, &telegram.DeleteMyCommandsParams{Scope: scope})
		g.logMenuError(ctx, b, "failed to remove the command menu", err)
	}
}

func (g *gateway) logMenuError(ctx context.Context, b *bot, msg string, err error) {
	if err == nil || ctx.Err() != nil {
		return
	}

	g.log.WarnContext(ctx, "telegram gateway: "+msg, "bot", b.role, "error", err)
}
