package s3

import (
	"go.uber.org/fx"

	"github.com/Prokud1n/ai-agents/internal/health"
)

var Module = fx.Module("s3",
	fx.Provide(
		NewClient,
		fx.Annotate(asChecker, fx.ResultTags(`group:"readiness"`)),
	),
)

// asChecker registers the client as a /readyz dependency.
func asChecker(c *Client) health.Checker {
	return c
}
