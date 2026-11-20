package tasks

import (
	"go.uber.org/fx"

	"github.com/Prokud1n/ai-agents/internal/agents"
	"github.com/Prokud1n/ai-agents/internal/knowledge"
	"github.com/Prokud1n/ai-agents/internal/llmusage"
)

// Module provides the task service and starts the workers. It needs a Notifier: LogNotifierModule until
// the Telegram gateway provides its own.
var Module = fx.Module("tasks",
	fx.Provide(
		NewService,
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

// registerPool starts the workers; on stop the workers stop first, then background work of the service
// (corrections being saved) is waited for.
func registerPool(lc fx.Lifecycle, p *Pool, s *Service) {
	lc.Append(fx.Hook{OnStop: s.WaitBackground})
	lc.Append(fx.Hook{OnStart: p.Start, OnStop: p.Stop})
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
