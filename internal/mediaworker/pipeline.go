// Package mediaworker is the media worker (cmd/media-worker): it takes the editor's jobs from the board
// (internal/editjob) and edits the source videos with ffmpeg, in a container of its own (the media image), with
// access to S3, Redis and the speech recognition API only.
//
// One job is the deterministic pipeline: download the source from S3 → check it (a picture, sound, the duration
// limit) → transcribe it → plan the rough cut (pauses, fillers, failed takes; disputed repeats are kept, the next
// step decides them) → render the cut, the brand subtitles and the Instagram export in one ffmpeg pass → upload
// the reel, the plan, the decisions log and the source transcript to S3. The job's directory is removed after it.
package mediaworker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/editjob"
	"github.com/Prokud1n/ai-agents/internal/media"
	"github.com/Prokud1n/ai-agents/internal/providers/ffmpeg"
	"github.com/Prokud1n/ai-agents/internal/providers/whisper"
	"github.com/Prokud1n/ai-agents/internal/roughcut"
	"github.com/Prokud1n/ai-agents/internal/subtitles"
	"github.com/Prokud1n/ai-agents/internal/transcription"
)

const (
	// JobDirPattern is the directory of one job in MEDIA_WORKER_DIR.
	JobDirPattern  = "job-*"
	cutDir         = "cut"
	reelFile       = "reel.mp4"
	transcriptFile = "source-transcript.json"

	insufficientQuota = "insufficient_quota"
	timePrecision     = 1000 // durations in the result are rounded to milliseconds
)

var (
	ErrInvalidConfig = errors.New("invalid media worker config")
	// errFontFallback: libass replaced a brand font.
	errFontFallback = errors.New("brand font replaced by libass")
	errNoVideo      = errors.New("source has no picture")
	errNoAudio      = errors.New("source has no sound")
	errTooLong      = errors.New("source is longer than the limit")
	errNoWords      = errors.New("transcript has no words")
)

type store interface {
	Download(ctx context.Context, key, dir string, maxSize int64) (*media.LocalFile, error)
	PutResult(ctx context.Context, key string, r io.ReadSeeker, contentType string) error
	ResultKey(sourceKey, name, ext string) (string, error)
}

type prober interface {
	Probe(ctx context.Context, path string) (ffmpeg.MediaInfo, error)
}

type keyChecker interface {
	CheckAPIKey() error
}

type transcriber interface {
	Transcribe(ctx context.Context, data transcription.TranscribeData) (*transcription.Result, error)
}

type cutter interface {
	Cut(ctx context.Context, data *roughcut.CutData) (*roughcut.Result, error)
}

type burner interface {
	BurnSubtitles(ctx context.Context, data *subtitles.BurnSubtitlesData) (*subtitles.BurnResult, error)
}

// JobError is a failed step of a job with the code the app shows the trainer by.
type JobError struct {
	Code editjob.FailureCode
	Err  error
}

func (e *JobError) Error() string {
	return string(e.Code) + ": " + e.Err.Error()
}

func (e *JobError) Unwrap() error {
	return e.Err
}

func fail(code editjob.FailureCode, err error) error {
	return &JobError{Code: code, Err: err}
}

// CodeOf is the failure code of a job error; any error without one is temporary.
func CodeOf(err error) editjob.FailureCode {
	var jobErr *JobError
	if errors.As(err, &jobErr) {
		return jobErr.Code
	}

	return editjob.TemporaryCode
}

// PipelineParams are the tools of the pipeline.
type PipelineParams struct {
	Store       store
	Prober      prober
	Keys        keyChecker
	Transcriber transcriber
	Cutter      cutter
	Burner      burner
	Config      *config.MediaWorker
	Log         *slog.Logger
}

// Pipeline edits one source video into a reel; see the package description.
type Pipeline struct {
	store       store
	prober      prober
	keys        keyChecker
	transcriber transcriber
	cutter      cutter
	burner      burner
	workDir     string
	maxDuration float64
	maxSize     int64
	fit         ffmpeg.FitMode
	log         *slog.Logger
	now         func() time.Time
}

// NewPipeline fails on invalid MEDIA_WORKER_* values, so a typo stops the start.
func NewPipeline(p *PipelineParams) (*Pipeline, error) {
	cfg := p.Config

	fit := ffmpeg.FitMode(cfg.Fit)
	if fit != ffmpeg.PadFitMode && fit != ffmpeg.CropFitMode {
		return nil, fmt.Errorf("MEDIA_WORKER_FIT %q must be pad or crop: %w", cfg.Fit, ErrInvalidConfig)
	}

	if cfg.MaxSourceDuration <= 0 || cfg.MaxSourceSize <= 0 {
		return nil, fmt.Errorf("MEDIA_WORKER_MAX_SOURCE_DURATION and _SIZE must be positive: %w", ErrInvalidConfig)
	}

	return &Pipeline{
		store: p.Store, prober: p.Prober, keys: p.Keys, transcriber: p.Transcriber, cutter: p.Cutter, burner: p.Burner,
		workDir: cfg.WorkDir, maxDuration: cfg.MaxSourceDuration.Seconds(), maxSize: cfg.MaxSourceSize, fit: fit,
		log: p.Log, now: time.Now,
	}, nil
}

// Run edits the source of the job and uploads the files (editjob.Files). The result has the numbers known so far
// even with an error; the error's code (CodeOf) tells why the job failed.
func (p *Pipeline) Run(ctx context.Context, job *editjob.Job) (*editjob.Result, error) {
	result := &editjob.Result{JobID: job.ID}

	// Without the key nothing is worth downloading: the trainer is told at once that editing is not configured.
	if err := p.keys.CheckAPIKey(); err != nil {
		return result, fail(editjob.NotConfiguredCode, err)
	}

	files, err := editjob.Files(p.store, job.SourceKey, job.Version)
	if err != nil {
		return result, fail(editjob.SourceMissingCode, err)
	}

	dir, err := os.MkdirTemp(p.workDir, JobDirPattern)
	if err != nil {
		return result, fmt.Errorf("failed to create job dir: %w", err)
	}

	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			p.log.ErrorContext(ctx, "mediaworker: failed to remove job dir", "error", err, "job_id", job.ID)
		}
	}()

	work := &jobWork{job: job, dir: dir, result: result, files: &files}
	for _, step := range []struct {
		name string
		run  func(ctx context.Context, w *jobWork) error
	}{
		{name: "download", run: p.download},
		{name: "probe", run: p.check},
		{name: "transcribe", run: p.transcribe},
		{name: "plan", run: p.cut},
		{name: "render", run: p.render},
		{name: "upload", run: p.upload},
	} {
		started := p.now()

		if err := step.run(ctx, work); err != nil {
			return result, fmt.Errorf("failed to %s: %w", step.name, err)
		}

		p.log.InfoContext(ctx, "mediaworker: step done", "job_id", job.ID, "step", step.name,
			"seconds", roundTime(p.now().Sub(started).Seconds()))
	}

	result.Status = editjob.DoneStatus
	result.FinishedAt = p.now()

	return result, nil
}

// jobWork is what the steps of one job pass on.
type jobWork struct {
	job        *editjob.Job
	dir        string
	result     *editjob.Result
	files      *editjob.ResultFiles
	source     string
	transcript *transcription.Result
	cut        *roughcut.Result
	reel       string
}

func (p *Pipeline) download(ctx context.Context, w *jobWork) error {
	local, err := p.store.Download(ctx, w.job.SourceKey, w.dir, p.maxSize)

	switch {
	case errors.Is(err, media.ErrFileNotFound) || errors.Is(err, media.ErrInvalidKey):
		return fail(editjob.SourceMissingCode, err)
	case errors.Is(err, media.ErrFileTooLarge):
		return fail(editjob.SourceTooLargeCode, err)
	case err != nil:
		return err
	}

	w.source = local.Path

	return nil
}

// check refuses a source without a picture or sound, or above the duration limit, before it is transcribed.
func (p *Pipeline) check(ctx context.Context, w *jobWork) error {
	info, err := p.prober.Probe(ctx, w.source)
	if err != nil {
		if ctx.Err() != nil {
			return err
		}

		return fail(editjob.BadSourceCode, err)
	}

	w.result.SourceDuration = roundTime(info.Duration)

	switch {
	case !info.HasVideo:
		return fail(editjob.BadSourceCode, errNoVideo)
	case !info.HasAudio:
		return fail(editjob.NoAudioCode, errNoAudio)
	case info.Duration > p.maxDuration:
		w.result.MaxDuration = p.maxDuration

		return fail(editjob.SourceTooLongCode, fmt.Errorf("%.1f s, at most %.0f s: %w", info.Duration, p.maxDuration, errTooLong))
	}

	return nil
}

func (p *Pipeline) transcribe(ctx context.Context, w *jobWork) error {
	transcript, err := p.transcriber.Transcribe(ctx, transcription.TranscribeData{Path: w.source})
	if err != nil {
		return classifyTranscription(err)
	}

	w.transcript = transcript
	w.result.AudioSeconds = transcript.Duration

	if len(transcript.Words) == 0 {
		return fail(editjob.NoSpeechCode, errNoWords)
	}

	return nil
}

// classifyTranscription: a missing key is configuration, a refused key or account cannot be fixed by a retry,
// anything else (the network, 5xx after the client's own retries) may pass.
func classifyTranscription(err error) error {
	var status *whisper.StatusError

	switch {
	case errors.Is(err, whisper.ErrAPIKeyMissing):
		return fail(editjob.NotConfiguredCode, err)
	case errors.As(err, &status) && (status.StatusCode == http.StatusUnauthorized ||
		status.StatusCode == http.StatusForbidden || status.Code == insufficientQuota):
		return fail(editjob.TranscriptionRejectedCode, err)
	default:
		return err
	}
}

// cut plans the rough cut with the default settings; disputed repeats (Plan.Candidates) are kept.
func (p *Pipeline) cut(ctx context.Context, w *jobWork) error {
	cut, err := p.cutter.Cut(ctx, &roughcut.CutData{
		Source: w.source, Transcript: w.transcript, OutDir: filepath.Join(w.dir, cutDir), PlanOnly: true,
	})

	switch {
	case errors.Is(err, roughcut.ErrNoAudio):
		return fail(editjob.NoAudioCode, err)
	case errors.Is(err, roughcut.ErrNoVideo) || errors.Is(err, roughcut.ErrNoFrameRate):
		return fail(editjob.BadSourceCode, err)
	case errors.Is(err, roughcut.ErrInvalidPlan):
		return fail(editjob.NoSpeechCode, err)
	case err != nil:
		return err
	}

	w.cut = cut
	w.result.Duration = cut.Plan.Duration
	w.result.Cuts = len(cut.Plan.Cuts)
	w.result.Retakes = len(cut.Plan.Takes)
	w.result.Candidates = len(cut.Plan.Candidates)

	return nil
}

// render cuts, subtitles and exports in one ffmpeg pass: the source is decoded once and the reel encoded once.
func (p *Pipeline) render(ctx context.Context, w *jobWork) error {
	w.reel = filepath.Join(w.dir, reelFile)

	burn, err := p.burner.BurnSubtitles(ctx, &subtitles.BurnSubtitlesData{
		CreateSubtitlesData: subtitles.CreateSubtitlesData{Transcript: &w.cut.Transcript},
		VideoPath:           w.source,
		OutputPath:          w.reel,
		Fit:                 p.fit,
		Cut:                 &ffmpeg.Cut{Segments: w.cut.Segments(), Options: w.cut.Render},
	})
	if err != nil {
		return err
	}

	if len(burn.FontFallbacks) > 0 {
		p.log.ErrorContext(ctx, "mediaworker: brand fonts not found, check SUBTITLES_FONTS_DIR",
			"job_id", w.job.ID, "fallbacks", burn.FontFallbacks)

		return fail(editjob.FontsCode, errFontFallback)
	}

	return nil
}

// upload stores the files of the job; the reel goes last, so a reel in S3 always has its plan next to it.
func (p *Pipeline) upload(ctx context.Context, w *jobWork) error {
	transcript := filepath.Join(w.dir, transcriptFile)
	if err := writeJSON(transcript, w.transcript); err != nil {
		return err
	}

	for _, f := range []struct{ key, path, contentType string }{
		{key: w.files.Plan, path: w.cut.Files.Plan, contentType: editjob.JSONContentType},
		{key: w.files.Decisions, path: w.cut.Files.Log, contentType: editjob.LogContentType},
		{key: w.files.Transcript, path: transcript, contentType: editjob.JSONContentType},
		{key: w.files.Reel, path: w.reel, contentType: editjob.VideoContentType},
	} {
		if err := p.put(ctx, f.key, f.path, f.contentType); err != nil {
			return err
		}
	}

	return nil
}

func (p *Pipeline) put(ctx context.Context, key, path, contentType string) error {
	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("failed to open result file: %w", err)
	}
	defer file.Close()

	return p.store.PutResult(ctx, key, file, contentType)
}

func writeJSON(path string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("failed to encode %s: %w", filepath.Base(path), err)
	}

	if err := os.WriteFile(path, data, resultFileMode); err != nil {
		return fmt.Errorf("failed to write %s: %w", filepath.Base(path), err)
	}

	return nil
}

const resultFileMode = 0o600

func roundTime(seconds float64) float64 {
	return math.Round(seconds*timePrecision) / timePrecision
}
