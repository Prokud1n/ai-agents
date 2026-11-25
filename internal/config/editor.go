package config

import (
	"fmt"
	"time"

	"github.com/caarlos0/env/v11"
	"go.uber.org/fx"
)

// Editor configures the editor's jobs: the app (internal/editor) gives them to the media worker
// (cmd/media-worker) through Redis and waits for the results. Both read it.
type Editor struct {
	// Stream is the Redis stream of the jobs; the job states and results are keys starting with it.
	Stream string `env:"EDITOR_STREAM" envDefault:"editor:jobs"`
	// JobTimeout bounds one edit from the moment a worker took it; a longer one fails with a timeout. The backlog
	// criterion is 30 minutes for a 3-minute source; 60m holds the render of a 10-minute 4K60 source on 2 CPUs
	// (MEDIA_WORKER_FFMPEG_TIMEOUT 40m) with the download, the transcription and the upload.
	JobTimeout time.Duration `env:"EDITOR_JOB_TIMEOUT" envDefault:"60m"`
	// QueueTimeout bounds waiting in the queue before a worker takes the job: several sources in a row are edited one
	// by one, so it is much longer than JobTimeout.
	QueueTimeout time.Duration `env:"EDITOR_QUEUE_TIMEOUT" envDefault:"3h"`
	// WorkerWait: a job not taken this long after it was submitted, while no worker has sent a heartbeat (the
	// media-worker service is not running), fails at once instead of waiting for QueueTimeout.
	WorkerWait time.Duration `env:"EDITOR_WORKER_WAIT" envDefault:"3m"`
	// PollInterval is how often the app checks a job whose result has not come; usually the worker wakes the
	// task as soon as the result is stored, so this is only a fallback.
	PollInterval time.Duration `env:"EDITOR_POLL_INTERVAL" envDefault:"1m"`
	// ReelsMaxDuration is the longest video Instagram publishes as a reel (3 minutes since January 2025); a longer
	// draft (a workout is not shortened) gets a note in its description, it is not refused. Read by the app only.
	ReelsMaxDuration time.Duration `env:"EDITOR_REELS_MAX_DURATION" envDefault:"3m"`
}

// MediaWorker configures the media worker (cmd/media-worker): it edits videos with ffmpeg in a container of its
// own, without bot tokens, the Claude API key or the database.
type MediaWorker struct {
	// WorkDir holds the files of the current job (a directory per job, removed after it). Empty: the system
	// temporary directory. The media image sets /work.
	WorkDir string `env:"MEDIA_WORKER_DIR"`
	// MaxSourceDuration: a longer source is refused before it is transcribed.
	MaxSourceDuration time.Duration `env:"MEDIA_WORKER_MAX_SOURCE_DURATION" envDefault:"10m"`
	// MaxSourceSize in bytes: a larger object in S3 is not downloaded. Default 2 GiB, the Telegram limit.
	MaxSourceSize int64 `env:"MEDIA_WORKER_MAX_SOURCE_SIZE" envDefault:"2147483648"`
	// Fit brings a source that is not 9:16 to the 1080×1920 frame without distortion: pad (default) keeps the whole
	// picture and fills the rest with black, crop fills the frame and cuts off the edges.
	Fit string `env:"MEDIA_WORKER_FIT" envDefault:"pad"`
	// MinSpeechShare: an auto job whose talking-head cut would keep less than this share of the source (the speech
	// with its short pauses) stops for the editor's decision even without a caption or disputed repeats — it may be
	// a workout, where cutting the silence cuts the exercises — and its plan is the safe workout one (nothing cut).
	// 0 turns the check off: then only a source without words is decided. Why 0.6 — docs/SETUP.md.
	MinSpeechShare float64 `env:"MEDIA_WORKER_MIN_SPEECH_SHARE" envDefault:"0.6"`
	// HeartbeatFile is touched while the worker is healthy (it reaches Redis); the container healthcheck
	// (media-worker healthcheck) reads its age. Empty: <WorkDir>/.heartbeat.
	HeartbeatFile string `env:"MEDIA_WORKER_HEARTBEAT_FILE"`
}

// MediaWorkerCommand is the config of the media worker: Redis, S3, the editing tools and nothing of the app
// (no Telegram, Claude API or PostgreSQL settings).
type MediaWorkerCommand struct {
	fx.Out

	Logging       Logging
	Redis         *Redis         `env:",init"`
	S3            *S3            `env:",init"`
	Media         *Media         `env:",init"`
	OpenAI        *OpenAI        `env:",init"`
	Whisper       *Whisper       `env:",init"`
	FFmpeg        *FFmpeg        `env:",init"`
	Transcription *Transcription `env:",init"`
	RoughCut      *RoughCut      `env:",init"`
	Subtitles     *Subtitles     `env:",init"`
	Editor        *Editor        `env:",init"`
	MediaWorker   *MediaWorker   `env:",init"`
}

func ParseMediaWorker() (MediaWorkerCommand, error) {
	var cfg MediaWorkerCommand

	if err := env.Parse(&cfg); err != nil {
		return MediaWorkerCommand{}, fmt.Errorf("failed to parse media worker config: %w", err)
	}

	return cfg, nil
}
