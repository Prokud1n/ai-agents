package ffmpeg

import "go.uber.org/fx"

// Module provides the ffmpeg runner. The binaries are looked up in PATH on each call, so the module
// does not fail the start where ffmpeg is not installed (the distroless app image).
var Module = fx.Module("ffmpeg",
	fx.Provide(NewRunner),
)
