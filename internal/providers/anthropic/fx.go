package anthropic

import "go.uber.org/fx"

// Module provides the Claude API client. It is not a readiness dependency on purpose:
// an external API outage must not fail /readyz.
var Module = fx.Module("anthropic",
	fx.Provide(NewClient),
)
