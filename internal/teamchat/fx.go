package teamchat

import (
	"log/slog"

	"go.uber.org/fx"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/gateway"
	"github.com/Prokud1n/ai-agents/internal/tasks"
)

// Module connects the gateway and the tasks; it needs gateway.Module and tasks.Module and replaces
// tasks.LogNotifierModule. Events of the gateway go to Handler instead of gateway.AckHandler. tasks.Notifier
// is Notifier when the gateway is on (bot tokens are set), otherwise tasks.LogNotifier: without Telegram the
// app starts as before and drafts go to the log.
//
// The decoration is outside fx.Module on purpose: a decorator applies to its own scope and the scopes below it,
// so only at the level of the application options does it reach gateway.Module.
var Module = fx.Options(
	fx.Module("teamchat",
		fx.Provide(newHandler, newNotifier),
	),
	fx.Decorate(useHandler),
)

// newHandler registers waiting for the reactions set in the background when the handler is constructed: the
// gateway that calls the handler is constructed after it, so on stop the gateway stops first and no new
// reaction starts while they are waited for.
func newHandler(lc fx.Lifecycle, service *tasks.Service, sender *gateway.Sender, log *slog.Logger) *Handler {
	h := NewHandler(service, sender, log)
	lc.Append(fx.Hook{OnStop: h.WaitBackground})

	return h
}

func newNotifier(cfg *config.Telegram, sender *gateway.Sender, log *slog.Logger) tasks.Notifier {
	if !cfg.Enabled() {
		return tasks.NewLogNotifier(log)
	}

	return NewNotifier(sender, log)
}

// useHandler replaces the gateway's default handler.
func useHandler(h *Handler) gateway.Handler {
	return h
}
