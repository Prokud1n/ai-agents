package mediaworker

import (
	"log/slog"

	"go.uber.org/fx"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/editjob"
	"github.com/Prokud1n/ai-agents/internal/media"
	"github.com/Prokud1n/ai-agents/internal/providers/ffmpeg"
	"github.com/Prokud1n/ai-agents/internal/providers/whisper"
	"github.com/Prokud1n/ai-agents/internal/roughcut"
	"github.com/Prokud1n/ai-agents/internal/subtitles"
	"github.com/Prokud1n/ai-agents/internal/transcription"
)

// Module runs the worker; it needs editjob, media, ffmpeg, whisper, transcription, roughcut and subtitles.
var Module = fx.Module("mediaworker",
	fx.Provide(newPipeline, asWorkerBoard, asJobRunner, fx.Private),
	fx.Provide(NewWorker),
	fx.Invoke(registerWorker),
)

func newPipeline(
	files *media.Store,
	runner *ffmpeg.Runner,
	keys *whisper.Client,
	speech *transcription.Service,
	cut *roughcut.Service,
	subs *subtitles.Service,
	cfg *config.MediaWorker,
	log *slog.Logger,
) (*Pipeline, error) {
	return NewPipeline(&PipelineParams{
		Store: files, Prober: runner, Keys: keys, Transcriber: speech, Cutter: cut, Burner: subs, Meter: runner, Config: cfg,
		Log: log,
	})
}

func asWorkerBoard(b *editjob.Board) workerBoard {
	return b
}

func asJobRunner(p *Pipeline) jobRunner {
	return p
}

func registerWorker(lc fx.Lifecycle, w *Worker) {
	lc.Append(fx.Hook{OnStart: w.Start, OnStop: w.Stop})
}
