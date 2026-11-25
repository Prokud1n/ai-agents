// Package mediaworker is the media worker (cmd/media-worker): it takes the editor's jobs from the board
// (internal/editjob) and edits the source videos with ffmpeg, in a container of its own (the media image), with
// access to S3, Redis and the speech recognition API only.
//
// One job is the deterministic pipeline: download the source from S3 → check it (a picture, sound, the duration
// limit) → transcribe it → plan the rough cut (pauses, fillers, failed takes; disputed repeats are kept) → render
// the cut, the brand subtitles and the Instagram export in one ffmpeg pass → upload the reel, the plan, the
// decisions log and the source transcript to S3. The job's directory is removed after it.
//
// Two stages (editjob.Stage): an auto job with something to decide (the trainer's caption, disputed repeats) stops
// after the plan with PlannedStatus and uploads only the transcript, the plan and the log; the app decides with the
// model and replaces it with a render job. A render job (the decided first version or a revision) reads the stored
// transcript instead of transcribing the source again and builds the plan with the editor's checked Edit. The worker
// never sees the trainer's text: the edit is word indices, candidate IDs and corrected subtitle words (checked by the
// app), plus the model's short notes that go only into decisions.log; no free text reaches ffmpeg or the subtitles
// but the corrected words.
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
	// maxTranscriptSize of the stored transcript a render job reads: a 10-minute source has well under 1 MB.
	maxTranscriptSize = 32 << 20
)

var (
	ErrInvalidConfig = errors.New("invalid media worker config")
	// errFontFallback: libass replaced a brand font.
	errFontFallback = errors.New("brand font replaced by libass")
	errNoVideo      = errors.New("source has no picture")
	errNoAudio      = errors.New("source has no sound")
	errTooLong      = errors.New("source is longer than the limit")
)

// resultFile is a local file of the job uploaded under its key.
type resultFile struct{ key, path, contentType string }

// step is one step of a job.
type step struct {
	name string
	run  func(ctx context.Context, w *jobWork) error
}

type store interface {
	Download(ctx context.Context, key, dir string, maxSize int64) (*media.LocalFile, error)
	PutResult(ctx context.Context, key string, r io.ReadSeeker, contentType string) error
	Read(ctx context.Context, key string, maxSize int64) ([]byte, error)
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
	// minSpeechShare: see config.MediaWorker.MinSpeechShare.
	minSpeechShare float64
	// sound and zoom of the drafts (MEDIA_WORKER_LOUDNESS, _DENOISE, _ZOOM).
	sound soundSettings
	log   *slog.Logger
	now   func() time.Time
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

	if cfg.MinSpeechShare < 0 || cfg.MinSpeechShare > 1 {
		return nil, fmt.Errorf("MEDIA_WORKER_MIN_SPEECH_SHARE %.2f must be within 0..1: %w", cfg.MinSpeechShare,
			ErrInvalidConfig)
	}

	sound, err := parseSoundSettings(cfg)
	if err != nil {
		return nil, err
	}

	return &Pipeline{
		store: p.Store, prober: p.Prober, keys: p.Keys, transcriber: p.Transcriber, cutter: p.Cutter, burner: p.Burner,
		workDir: cfg.WorkDir, maxDuration: cfg.MaxSourceDuration.Seconds(), maxSize: cfg.MaxSourceSize, fit: fit,
		minSpeechShare: cfg.MinSpeechShare, sound: sound, log: p.Log, now: time.Now,
	}, nil
}

// Run edits the source of the job and uploads the files (editjob.Files). The result has the numbers known so far
// even with an error; the error's code (CodeOf) tells why the job failed.
func (p *Pipeline) Run(ctx context.Context, job *editjob.Job) (*editjob.Result, error) {
	result := &editjob.Result{JobID: job.ID}
	render := job.EffectiveStage() == editjob.RenderStage

	// Without the key nothing is worth downloading: the trainer is told at once that editing is not configured.
	// A render reads the stored transcript and needs no key.
	if !render {
		if err := p.keys.CheckAPIKey(); err != nil {
			return result, fail(editjob.NotConfiguredCode, err)
		}
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
	for _, s := range p.steps(render) {
		started := p.now()

		if err := s.run(ctx, work); err != nil {
			return result, fmt.Errorf("failed to %s: %w", s.name, err)
		}

		p.log.InfoContext(ctx, "mediaworker: step done", "job_id", job.ID, "step", s.name,
			"seconds", roundTime(p.now().Sub(started).Seconds()))
	}

	result.Status = editjob.DoneStatus
	if work.planned {
		result.Status = editjob.PlannedStatus
	}

	result.FinishedAt = p.now()

	return result, nil
}

// steps of a job: a render job loads the stored transcript instead of transcribing the source.
func (p *Pipeline) steps(render bool) []step {
	speech := step{name: "transcribe", run: p.transcribe}
	if render {
		speech = step{name: "load transcript", run: p.loadTranscript}
	}

	return []step{
		{name: "download", run: p.download},
		{name: "probe", run: p.check},
		speech,
		{name: "plan", run: p.cut},
		{name: "render", run: p.render},
		{name: "upload", run: p.upload},
	}
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
	// hdr: the source is HLG/PQ and is tone mapped to SDR bt709.
	hdr bool
	// planned: the job stops after the plan, the editor decides on it (no reel).
	planned bool
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
	w.hdr = info.HDR()

	if w.hdr {
		p.log.InfoContext(ctx, "mediaworker: HDR source, tone mapping to SDR", "job_id", w.job.ID,
			"transfer", info.ColorTransfer, "primaries", info.ColorPrimaries)
	}

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

	// A video without words is not refused: it may be a workout shown in silence (see cut).
	if len(transcript.Words) == 0 {
		p.log.InfoContext(ctx, "mediaworker: no speech recognized", "job_id", w.job.ID)
	}

	return nil
}

// loadTranscript reads the transcript the auto stage stored: a render job does not transcribe the source again.
func (p *Pipeline) loadTranscript(ctx context.Context, w *jobWork) error {
	data, err := p.store.Read(ctx, w.files.Transcript, maxTranscriptSize)

	switch {
	case errors.Is(err, media.ErrFileNotFound) || errors.Is(err, media.ErrInvalidKey) ||
		errors.Is(err, media.ErrFileTooLarge):
		return fail(editjob.SourceMissingCode, err)
	case err != nil:
		return err
	}

	var transcript transcription.Result
	if err := json.Unmarshal(data, &transcript); err != nil {
		return fail(editjob.SourceMissingCode, fmt.Errorf("failed to decode the stored transcript: %w", err))
	}

	// No words is a workout shown in silence: the plan decides whether anything is left to render.
	w.transcript = &transcript

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

// cut plans the rough cut: an auto job with the default settings (disputed repeats are kept), a render job with the
// settings and the edit the editor decided on. An auto job with something to decide stops after it: the trainer's
// caption, the hook title (Job.Hook), disputed repeats, little speech, or a talking head longer than the reel's
// target after the cleaning (roughcut.LongerThanTarget). A source with little speech (autoPlan) may be a workout: cutting its silence would cut
// the exercises, so it is planned in the workout mode (nothing cut) and the editor decides; that plan is also what is
// rendered if the model cannot decide.
func (p *Pipeline) cut(ctx context.Context, w *jobWork) error {
	auto := w.job.EffectiveStage() == editjob.AutoStage

	var (
		cut       *roughcut.Result
		lowSpeech bool
		err       error
	)

	if auto {
		cut, lowSpeech, err = p.autoPlan(ctx, w)
	} else {
		cut, err = p.plan(ctx, w, w.job.Edit)
	}

	if err != nil {
		return err
	}

	w.cut = cut
	w.result.Duration = cut.Plan.Duration
	w.result.Cuts = len(cut.Plan.Cuts)
	w.result.Retakes = len(cut.Plan.Takes)
	w.result.Candidates = len(cut.Plan.Candidates)

	// A talking head longer than the target after the cleaning is shortened by the editor: the best phrases are picked.
	long := auto && !lowSpeech && roughcut.LongerThanTarget(cut.Plan.Duration, w.job.TargetDuration)

	if auto && (w.job.Decide || w.job.Hook || lowSpeech || long || len(cut.Plan.Candidates) > 0) {
		w.planned = true

		p.log.InfoContext(ctx, "mediaworker: plan ready, the editor decides before the render", "job_id", w.job.ID,
			"candidates", len(cut.Plan.Candidates), "caption", w.job.Decide, "hook", w.job.Hook,
			"little_speech", lowSpeech, "longer_than_target", long, "duration", cut.Plan.Duration,
			"target_duration", w.job.TargetDuration)
	}

	return nil
}

// autoPlan is the plan of an auto job: the talking-head cut, or the workout plan (nothing cut) when the talking-head
// cut keeps less than minSpeechShare of the source, cannot be built (every word a filler, too many fragments of a
// counted workout) or there are no words at all. The second result says the workout plan was taken.
func (p *Pipeline) autoPlan(ctx context.Context, w *jobWork) (*roughcut.Result, bool, error) {
	if len(w.transcript.Words) > 0 {
		cut, err := p.plan(ctx, w, w.job.Edit)

		switch {
		case errors.Is(err, roughcut.ErrInvalidPlan):
			p.log.InfoContext(ctx, "mediaworker: no talking-head cut, planned as a workout", "job_id", w.job.ID,
				"error", err)
		case err != nil:
			return nil, false, err
		default:
			p.log.InfoContext(ctx, "mediaworker: speech share", "job_id", w.job.ID,
				"speech_share", cut.Plan.SpeechShare, "min_speech_share", p.minSpeechShare)

			if cut.Plan.SpeechShare >= p.minSpeechShare {
				return cut, false, nil
			}
		}
	}

	cut, err := p.plan(ctx, w, workoutEdit())
	if err != nil {
		return nil, false, err
	}

	p.log.InfoContext(ctx, "mediaworker: little speech, planned as a workout", "job_id", w.job.ID,
		"speech_share", cut.Plan.SpeechShare, "min_speech_share", p.minSpeechShare, "words", len(w.transcript.Words))

	return cut, true, nil
}

// plan runs the rough cut in plan-only mode with the edit and maps its errors to the failure codes.
func (p *Pipeline) plan(ctx context.Context, w *jobWork, edit *roughcut.Edit) (*roughcut.Result, error) {
	cut, err := p.cutter.Cut(ctx, &roughcut.CutData{
		Source: w.source, Transcript: w.transcript, OutDir: filepath.Join(w.dir, cutDir), PlanOnly: true,
		Settings: w.job.Settings, Edit: edit, MinSpeechShare: p.minSpeechShare,
	})

	switch {
	case errors.Is(err, roughcut.ErrNoAudio):
		return nil, fail(editjob.NoAudioCode, err)
	case errors.Is(err, roughcut.ErrNoVideo) || errors.Is(err, roughcut.ErrNoFrameRate):
		return nil, fail(editjob.BadSourceCode, err)
	case errors.Is(err, roughcut.ErrInvalidPlan) && w.job.EffectiveStage() == editjob.RenderStage:
		return nil, fail(editjob.InvalidEditCode, err)
	case errors.Is(err, roughcut.ErrInvalidPlan):
		return nil, fail(editjob.NoSpeechCode, err)
	case err != nil:
		return nil, err
	}

	return cut, nil
}

// workoutEdit is the edit of the safe plan of a source with little speech: the workout mode, nothing cut.
func workoutEdit() *roughcut.Edit {
	return &roughcut.Edit{Mode: roughcut.WorkoutMode}
}

// render cuts, subtitles and exports in one ffmpeg pass: the source is decoded once and the reel encoded once.
// A planned job is not rendered.
func (p *Pipeline) render(ctx context.Context, w *jobWork) error {
	if w.planned {
		return nil
	}

	w.reel = filepath.Join(w.dir, reelFile)

	// Without the sound of the source its speech is not heard: no speech subtitles, the titles stay.
	speech := &w.cut.Transcript
	if w.cut.Render.Mute {
		speech = &transcription.Result{Language: w.cut.Transcript.Language, Duration: w.cut.Transcript.Duration}
	}

	cut := p.sound.cut(w.cut, w.transcript.Loudness)

	burn, err := p.burner.BurnSubtitles(ctx, &subtitles.BurnSubtitlesData{
		CreateSubtitlesData: subtitles.CreateSubtitlesData{
			Transcript: speech, Titles: subtitleTitles(w.cut.Titles), Hook: editHook(w.cut.Plan.Edit),
			Notes: subtitleNotes(w.cut.ScreenNotes), Branding: p.branding(ctx, w.job),
		},
		VideoPath:  w.source,
		OutputPath: w.reel,
		Fit:        p.fit,
		Cut:        cut,
		ToneMap:    w.hdr,
	})
	if err != nil {
		return err
	}

	notes := p.sound.notes(cut, w.transcript.Loudness, w.cut.Plan.Mode)
	p.log.InfoContext(ctx, "mediaworker: sound and framing", "job_id", w.job.ID, "notes", notes)

	if err := appendDecisions(w.cut.Files.Log, notes); err != nil {
		return err
	}

	if len(burn.FontFallbacks) > 0 {
		p.log.ErrorContext(ctx, "mediaworker: brand fonts not found, check SUBTITLES_FONTS_DIR",
			"job_id", w.job.ID, "fallbacks", burn.FontFallbacks)

		return fail(editjob.FontsCode, errFontFallback)
	}

	return nil
}

// editHook is the hook title of the edit, "" without one: its text was checked by the app.
func editHook(edit *roughcut.Edit) string {
	if edit == nil {
		return ""
	}

	return edit.Hook
}

// subtitleNotes are the screen notes of the cut (on its timeline) for the subtitles: their text was checked by the app.
func subtitleNotes(notes []roughcut.ScreenNote) []subtitles.Note {
	out := make([]subtitles.Note, 0, len(notes))
	for _, n := range notes {
		out = append(out, subtitles.Note{Kind: subtitles.NoteKind(n.Kind), Text: n.Text, Start: n.Start, End: n.End})
	}

	return out
}

// branding is the handle and the card of the job for the subtitles; nil without one or with a handle that fails the
// check (the app checked it: a bad one here is logged and not drawn, the draft is made without it).
func (p *Pipeline) branding(ctx context.Context, job *editjob.Job) *subtitles.Branding {
	if job.Branding == nil {
		return nil
	}

	if err := job.Branding.Validate(); err != nil {
		p.log.ErrorContext(ctx, "mediaworker: the handle of the job is invalid, the draft is made without it",
			"job_id", job.ID, "error", err)

		return nil
	}

	return &subtitles.Branding{Handle: job.Branding.Handle, Icon: job.Branding.Icon, EndCard: job.Branding.EndCard}
}

// subtitleTitles are the exercise titles of the cut for the subtitles.
func subtitleTitles(titles []roughcut.Title) []subtitles.Title {
	out := make([]subtitles.Title, 0, len(titles))
	for _, t := range titles {
		out = append(out, subtitles.Title{Text: t.Text, Start: t.Start, End: t.End})
	}

	return out
}

// upload stores the files of the job; the reel goes last, so a reel in S3 always has its plan next to it. The
// transcript is stored by the auto stage only (a render job read it); a planned job has no reel.
func (p *Pipeline) upload(ctx context.Context, w *jobWork) error {
	uploads := []resultFile{
		{key: w.files.Plan, path: w.cut.Files.Plan, contentType: editjob.JSONContentType},
		{key: w.files.Decisions, path: w.cut.Files.Log, contentType: editjob.LogContentType},
	}

	if w.job.EffectiveStage() == editjob.AutoStage {
		transcript := filepath.Join(w.dir, transcriptFile)
		if err := writeJSON(transcript, w.transcript); err != nil {
			return err
		}

		uploads = append(uploads, resultFile{
			key: w.files.Transcript, path: transcript, contentType: editjob.JSONContentType,
		})
	}

	if !w.planned {
		uploads = append(uploads, resultFile{
			key: w.files.Reel, path: w.reel, contentType: editjob.VideoContentType,
		})
	}

	for _, f := range uploads {
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
