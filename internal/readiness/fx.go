// Package readiness is the adapter between provider clients and /readyz: it registers each client
// in the fx value group "readiness" read by the health package. Providers only implement
// Ping(ctx) error and know nothing about health checks.
package readiness

import (
	"go.uber.org/fx"

	"github.com/Prokud1n/ai-agents/internal/gateway"
	"github.com/Prokud1n/ai-agents/internal/health"
	"github.com/Prokud1n/ai-agents/internal/providers/postgres"
	"github.com/Prokud1n/ai-agents/internal/providers/redis"
	"github.com/Prokud1n/ai-agents/internal/providers/s3"
)

const groupTag = `group:"readiness"`

var Module = fx.Module("readiness",
	fx.Provide(
		check[*postgres.Client]("postgres"),
		check[*redis.Client]("redis"),
		check[*s3.Client]("s3"),
		// Fails only when Telegram rejected a bot token; without tokens it is always ok.
		check[*gateway.Status]("telegram"),
	),
)

// check registers the client of type T under name (the dependency name in /readyz).
func check[T health.Pinger](name string) any {
	return fx.Annotate(
		func(client T) health.Checker {
			return health.NewChecker(name, client)
		},
		fx.ResultTags(groupTag),
	)
}
