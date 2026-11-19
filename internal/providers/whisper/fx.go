package whisper

import "go.uber.org/fx"

// Module provides the Whisper client. It is not a readiness dependency: without OPENAI_API_KEY
// the application starts and only transcription calls fail with ErrAPIKeyMissing.
var Module = fx.Module("whisper",
	fx.Provide(NewClient),
)
