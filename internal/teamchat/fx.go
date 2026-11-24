package teamchat

import (
	"context"
	"log/slog"
	"time"

	"go.uber.org/fx"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/gateway"
	"github.com/Prokud1n/ai-agents/internal/ideas"
	"github.com/Prokud1n/ai-agents/internal/media"
	"github.com/Prokud1n/ai-agents/internal/pilot"
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
//
// The command menu of the group (gateway.Menu) is replaced the same way. The weekly pilot report runs when
// PILOT_REPORT_WEEKDAY and PILOT_REPORT_TIME are set; the module needs pilot.Module.
var Module = fx.Options(
	fx.Module("teamchat",
		fx.Provide(newHandler, newNotifier, asMediaStore, newTechReporter, newWeeklyReport),
		fx.Invoke(func(*WeeklyReport) {}),
	),
	fx.Decorate(useHandler, useMenu),
)

type handlerParams struct {
	fx.In

	Lifecycle fx.Lifecycle
	Config    *config.Telegram
	Service   *tasks.Service
	Ideas     *ideas.Service
	Sender    *gateway.Sender
	Files     mediaStore
	Log       *slog.Logger
}

// newHandler registers waiting for the reactions set in the background when the handler is constructed: the
// gateway that calls the handler is constructed after it, so on stop the gateway stops first and no new
// reaction starts while they are waited for.
func newHandler(p handlerParams, team *config.Team, store *pilot.Service, tech *TechReporter) (*Handler, error) {
	location, err := team.Location()
	if err != nil {
		return nil, err
	}

	h := NewHandler(p.Service, p.Ideas, p.Sender, p.Files, p.Log)
	h.location = location
	h.productionTopicID = p.Config.ProductionTopicID
	h.maxFileSize = p.Config.MaxFileSize
	h.pilot = store
	h.tech = tech
	h.admins = adminSet(p.Config.AdminUserIDs)
	h.greetings = greetings(p.Config)
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

// useMenu replaces the gateway's empty command menu.
func useMenu() gateway.Menu {
	return gateway.Menu{Commands: menuCommands}
}

// newWeeklyReport schedules the weekly pilot report; without a schedule it does nothing. A schedule set by half or with
// a typo stops the start.
func newWeeklyReport(
	lc fx.Lifecycle,
	cfg *config.Pilot,
	team *config.Team,
	store *pilot.Service,
	tech *TechReporter,
	log *slog.Logger,
) (*WeeklyReport, error) {
	r := &WeeklyReport{store: store, tech: tech, log: log, now: time.Now}

	if !cfg.ReportEnabled() {
		log.Info("teamchat: weekly pilot report is off: PILOT_REPORT_WEEKDAY and PILOT_REPORT_TIME are not set")

		return r, nil
	}

	location, err := team.Location()
	if err != nil {
		return nil, err
	}

	r.schedule, err = parseSchedule(cfg, location)
	if err != nil {
		return nil, err
	}

	lc.Append(fx.Hook{OnStart: r.Start, OnStop: r.Stop})

	return r, nil
}

// useHandler replaces the gateway's default handler.
func useHandler(h *Handler) gateway.Handler {
	return h
}

func newTechReporter(cfg *config.Telegram, sender *gateway.Sender, log *slog.Logger) *TechReporter {
	return NewTechReporter(cfg, sender, log)
}
