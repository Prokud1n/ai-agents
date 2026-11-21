package tasks

import (
	"log/slog"

	"go.uber.org/fx"

	"github.com/Prokud1n/ai-agents/internal/agents"
	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/knowledge"
	"github.com/Prokud1n/ai-agents/internal/llmusage"
)

// Module provides the task service and starts the workers. It needs a Notifier: in the application
// teamchat.Module provides it (Telegram, or the log without bot tokens); LogNotifierModule only logs.
var Module = fx.Module("tasks",
	fx.Provide(
		provideService,
		fx.Annotate(NewAgentExecutor, fx.As(new(Executor))),
	),
	fx.Provide(
		newRepo,
		newRedisQueue,
		NewPool,
		asEnqueuer,
		asWorkerQueue,
		asTaskRunner,
		asAgentRunner,
		asCorrectionSaver,
		asUsageSummarizer,
		fx.Private,
	),
	fx.Invoke(registerPool),
)

// LogNotifierModule provides a Notifier that only logs, so the app runs without Telegram.
var LogNotifierModule = fx.Module("tasks-log-notifier",
	fx.Provide(fx.Annotate(NewLogNotifier, fx.As(new(Notifier)))),
)

// registerPool starts the workers. They are registered after the service, so on stop they stop before the
// service waits for its background work.
func registerPool(lc fx.Lifecycle, p *Pool) {
	lc.Append(fx.Hook{OnStart: p.Start, OnStop: p.Stop})
}

// provideService registers waiting for the service's background work (corrections being saved, drafts being
// closed) when the service is constructed. Everything that uses the service (the workers, the Telegram
// gateway) is constructed after it, and fx stops in the reverse order: they stop first, so nothing starts new
// background work while it is waited for.
func provideService(
	lc fx.Lifecycle,
	cfg *config.Tasks,
	r *repo,
	queue enqueuer,
	executor Executor,
	notifier Notifier,
	corrections correctionSaver,
	usage usageSummarizer,
	log *slog.Logger,
) (*Service, error) {
	s, err := NewService(cfg, r, queue, executor, notifier, corrections, usage, log)
	if err != nil {
		return nil, err
	}

	lc.Append(fx.Hook{OnStop: s.WaitBackground})

	return s, nil
}

func asEnqueuer(q *redisQueue) enqueuer {
	return q
}

func asWorkerQueue(q *redisQueue) workerQueue {
	return q
}

func asTaskRunner(s *Service) taskRunner {
	return s
}

func asAgentRunner(r *agents.Runtime) agentRunner {
	return r
}

func asCorrectionSaver(s *knowledge.Service) correctionSaver {
	return s
}

func asUsageSummarizer(s *llmusage.Service) usageSummarizer {
	return s
}
