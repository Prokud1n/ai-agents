package transcription

import (
	"go.uber.org/fx"

	"github.com/Prokud1n/ai-agents/internal/providers/ffmpeg"
	"github.com/Prokud1n/ai-agents/internal/providers/whisper"
)

// Module provides the transcription service; it needs ffmpeg.Module and whisper.Module.
var Module = fx.Module("transcription",
	fx.Provide(asAudioProcessor, asTranscriber, fx.Private),
	fx.Provide(NewService),
)

func asAudioProcessor(r *ffmpeg.Runner) audioProcessor {
	return r
}

func asTranscriber(c *whisper.Client) transcriber {
	return c
}
