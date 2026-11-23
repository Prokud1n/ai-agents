package media

import (
	"go.uber.org/fx"

	"github.com/Prokud1n/ai-agents/internal/providers/s3"
)

// Module provides the Store; it needs s3.Module.
var Module = fx.Module("media",
	fx.Provide(asObjectStorage, fx.Private),
	fx.Provide(NewStore),
)

func asObjectStorage(c *s3.Client) objectStorage {
	return c
}
