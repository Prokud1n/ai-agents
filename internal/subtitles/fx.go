package subtitles

import (
	"go.uber.org/fx"

	"github.com/Prokud1n/ai-agents/internal/providers/ffmpeg"
)

// Module provides the subtitles service; it needs ffmpeg.Module and *config.Subtitles.
var Module = fx.Module("subtitles",
	fx.Provide(asVideoRenderer, fx.Private),
	fx.Provide(NewService),
)

func asVideoRenderer(r *ffmpeg.Runner) videoRenderer {
	return r
}
