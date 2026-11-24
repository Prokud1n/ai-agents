package teamchat

import (
	"context"
	"log/slog"

	"go.uber.org/fx"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/gateway"
	"github.com/Prokud1n/ai-agents/internal/ideas"
	"github.com/Prokud1n/ai-agents/internal/media"
	"github.com/Prokud1n/ai-agents/internal/tasks"
)

// Module connects the gateway and the tasks; it needs gateway.Module, tasks.Module and ideas.Module and replaces
// tasks.LogNotifierModule. Events of the gateway go to Handler instead of gateway.AckHandler. tasks.Notifier
// is Notifier when the gateway is on (bot tokens are set), otherwise tasks.LogNotifier: without Telegram the
// app starts as before and drafts go to the log.
//
// The decoration is outside fx.Module on purpose: a decorator applies to its own scope and the scopes below it,
// so only at the level of the application options does it reach gateway.Module.
//
// Videos are stored with media.Module (needed too), and the editor's reels are taken from it for the drafts;
// mediaStore is provided outside fx.Private, so a test can replace it with fx.Decorate.
var Module = fx.Options(
	fx.Module("teamchat",
		fx.Provide(newHandler, newNotifier, asMediaStore, newTechReporter),
	),
	fx.Decorate(useHandler),
)

type handlerParams struct {
	fx.In

	Lifecycle fx.Lifecycle
	Config    *config.Telegram
	Team      *config.Team
	Service   *tasks.Service
	Ideas     *ideas.Service
	Sender    *gateway.Sender
	Files     mediaStore
	Log       *slog.Logger
}

// newHandler registers waiting for the reactions set in the background when the handler is constructed: the
// gateway that calls the handler is constructed after it, so on stop the gateway stops first and no new
// reaction starts while they are waited for.
func newHandler(p handlerParams) (*Handler, error) {
	location, err := p.Team.Location()
	if err != nil {
		return nil, err
	}

	h := NewHandler(p.Service, p.Ideas, p.Sender, p.Files, p.Log)
	h.location = location
	h.productionTopicID = p.Config.ProductionTopicID
	h.maxFileSize = p.Config.MaxFileSize
	p.Lifecycle.Append(fx.Hook{OnStop: h.WaitBackground})

	return h, nil
}

func asMediaStore(s *media.Store) mediaStore {
	return s
}

// newNotifier cleans the outbox on start of the reels a killed app left there (they are sent from it and removed after
// sending).
func newNotifier(
	lc fx.Lifecycle,
	cfg *config.Telegram,
	sender *gateway.Sender,
	files mediaStore,
	log *slog.Logger,
) tasks.Notifier {
	if !cfg.Enabled() {
		return tasks.NewLogNotifier(log)
	}

	lc.Append(fx.StartHook(func(ctx context.Context) {
		cleanOutbox(ctx, cfg.OutboxDir, log)
	}))

	n := NewNotifier(sender, log)
	n.videos = &videoDrafts{
		sender: sender, files: files, outboxDir: cfg.OutboxDir, maxFileSize: cfg.MaxFileSize,
		productionTopicID: cfg.ProductionTopicID,
	}

	return n
}

// useHandler replaces the gateway's default handler.
func useHandler(h *Handler) gateway.Handler {
	return h
}

func newTechReporter(cfg *config.Telegram, sender *gateway.Sender, log *slog.Logger) *TechReporter {
	return NewTechReporter(cfg, sender, log)
}
