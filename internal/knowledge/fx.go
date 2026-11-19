package knowledge

import (
	"go.uber.org/fx"

	"github.com/Prokud1n/ai-agents/internal/providers/embeddings"
)

var Module = fx.Module("knowledge",
	fx.Provide(NewService),
	fx.Provide(newRepo, fx.Private),
	fx.Provide(asEmbedder, fx.Private),
)

func asEmbedder(c *embeddings.Client) embedder {
	return c
}
