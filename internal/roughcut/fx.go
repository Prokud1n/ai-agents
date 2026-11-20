package roughcut

import (
	"go.uber.org/fx"

	"github.com/Prokud1n/ai-agents/internal/providers/ffmpeg"
)

// Module provides the rough cut service; it needs ffmpeg.Module and *config.RoughCut.
var Module = fx.Module("roughcut",
	fx.Provide(asMediaProcessor, fx.Private),
	fx.Provide(NewService),
)

func asMediaProcessor(r *ffmpeg.Runner) mediaProcessor {
	return r
}
