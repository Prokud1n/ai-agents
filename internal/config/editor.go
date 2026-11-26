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
	// criterion is 30 minutes for a 3-minute source; 70m holds the render of a 10-minute 4K60 source on 2 CPUs
	// (MEDIA_WORKER_FFMPEG_TIMEOUT 45m) with the download, the transcription and the upload.
	JobTimeout time.Duration `env:"EDITOR_JOB_TIMEOUT" envDefault:"70m"`
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
	// ReelsTargetDuration is the longest talking-head reel the editor aims at: a first version whose cleaned cut is
	// longer stops after the plan even without a caption or disputed repeats, and the editor's model picks the best
	// phrases for a reel of half this length up to it (30–60 s for 60s). 0 (the default: the trainer's reels are 2–3
	// minutes) turns the selection off; the trainer's caption or revision may still ask for a length. Read by the app;
	// the worker gets it in the job.
	ReelsTargetDuration time.Duration `env:"EDITOR_REELS_TARGET_DURATION" envDefault:"0s"`
	// HookTitle: the editor's model proposes a hook title for the first seconds of every first version, so every
	// first version stops after the plan for the model's decision (one more call of the model for a short reel that
	// had nothing else to decide). false: no hook unless the trainer asks for one, and a short reel without a caption
	// or disputed repeats is rendered at once, as before. Read by the app; the worker gets it in the job.
	HookTitle bool `env:"EDITOR_HOOK_TITLE" envDefault:"true"`
	// Cleanup: the editor's model cleans the first version of a talking head of the failed takes, a phrase said again
	// in a row and the preparation for the shooting, with or without a caption, so every first version stops after the
	// plan for the model's decision. false: only the automatic cut cleans it. Read by the app.
	Cleanup bool `env:"EDITOR_CLEANUP" envDefault:"true"`
	// ReelsHandle is the trainer's Instagram handle («@prokudina.ana»: «@» and 1–30 of a-z, 0-9, «.», «_»; anything
	// else fails the start) drawn small at the right edge over every draft and large on its end card. Empty (the
	// default): no handle and no card. Read by the app; the worker gets it in the job and checks it again.
	ReelsHandle string `env:"REELS_HANDLE"`
	// ReelsHandleIcon draws a simple Instagram icon (a rounded square, a circle and a dot) before the handle.
	ReelsHandleIcon bool `env:"REELS_HANDLE_ICON" envDefault:"true"`
	// ReelsEndCard darkens the last 2 seconds of the draft and shows the icon and the handle large in the middle (the
	// reel is not made longer, the sound stays).
	ReelsEndCard bool `env:"REELS_END_CARD" envDefault:"true"`
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
	// 0 turns the check off: then only a source without words is decided. Why 0.35 — docs/SETUP.md (measured: talking
	// heads 0.44–0.58, a workout 0.26, music 0.01).
	MinSpeechShare float64 `env:"MEDIA_WORKER_MIN_SPEECH_SHARE" envDefault:"0.35"`
	// Loudness the drafts are brought to: integrated loudness in LUFS (EBU R128; Instagram and other networks play
	// at about -14), the true peaks at most -1.5 dBTP. "off" (or empty) keeps the level of the source.
	Loudness string `env:"MEDIA_WORKER_LOUDNESS" envDefault:"-14"`
	// Denoise lightly reduces the noise of speech (the rumble and the stationary hiss) in a talking-head draft.
	Denoise bool `env:"MEDIA_WORKER_DENOISE" envDefault:"true"`
	// Zoom of the jump-cut zoom of a talking head, percent (the framing alternates at the joints); 0 turns it off.
	Zoom float64 `env:"MEDIA_WORKER_ZOOM" envDefault:"9"`
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
