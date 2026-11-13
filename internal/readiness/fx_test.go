package readiness

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"

	"github.com/Prokud1n/ai-agents/internal/health"
	"github.com/Prokud1n/ai-agents/internal/providers/postgres"
	"github.com/Prokud1n/ai-agents/internal/providers/redis"
	"github.com/Prokud1n/ai-agents/internal/providers/s3"
)

type checkers struct {
	fx.In

	All []health.Checker `group:"readiness"`
}

func TestModule(t *testing.T) {
	t.Parallel()

	var names []string

	app := fxtest.New(t,
		// Clients are never pinged here: the test checks only what is registered in the group.
		fx.Supply(new(postgres.Client), new(redis.Client), new(s3.Client)),
		Module,
		fx.Invoke(func(c checkers) {
			for _, checker := range c.All {
				names = append(names, checker.Name())
			}
		}),
	)
	app.RequireStart().RequireStop()

	assert.ElementsMatch(t, []string{"postgres", "redis", "s3"}, names)
}
