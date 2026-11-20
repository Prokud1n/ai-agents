package kbtool

import (
	"go.uber.org/fx"

	"github.com/Prokud1n/ai-agents/internal/agents"
	"github.com/Prokud1n/ai-agents/internal/knowledge"
	"github.com/Prokud1n/ai-agents/internal/providers/embeddings"
)

// Module adds search_knowledge_base to the agent tools (agents.ToolsGroup). It needs knowledge.Module
// and embeddings.Module. The tool is registered even without OPENAI_API_KEY: then it answers that the
// knowledge base is unavailable, and the agents registry warns on start about agents that list it.
var Module = fx.Module("kbtool",
	fx.Provide(asSearcher, asEmbeddingsStatus, fx.Private),
	fx.Provide(fx.Annotate(NewSearchTool, fx.As(new(agents.Tool)), fx.ResultTags(agents.ToolsGroup))),
)

func asSearcher(s *knowledge.Service) searcher {
	return s
}

func asEmbeddingsStatus(c *embeddings.Client) embeddingsStatus {
	return c
}
