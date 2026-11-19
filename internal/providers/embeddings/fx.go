package embeddings

import "go.uber.org/fx"

// Module provides the embeddings client. It is not a /readyz dependency: without OPENAI_API_KEY
// the app still starts, and calls return ErrNotConfigured.
var Module = fx.Module("embeddings",
	fx.Provide(NewClient),
)
