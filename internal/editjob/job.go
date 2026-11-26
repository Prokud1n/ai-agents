// Package editjob is the contract between the app and the media worker (cmd/media-worker): a job to edit
// a source video, its result and the Redis board where they meet.
//
// The app owns the tasks and Telegram; the worker runs ffmpeg over files that come from users, so it runs in a
// container of its own without bot tokens, the Claude API key or the database. The app submits a job (Board.Submit:
// a state key plus a stream entry), the worker takes it from the stream, edits the video, stores the files in S3
// under keys both sides compute from the source key (Files) and puts the result on the board (Board.Finish); the
// app reads it. No key, path or text of the app's messages comes from the worker: the result carries only numbers
// and a failure code.
//
// Idempotency: the job ID is made of the task and the draft version, so a repeated run of the task finds the same
// job. A job runs under a lock with a heartbeat; a delivery whose job has a result or a live lock is skipped, so a
// redelivered stream entry does not edit the video twice.
package editjob

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Prokud1n/ai-agents/internal/roughcut"
)

// Status of a finished job.
type Status string

const (
	DoneStatus   Status = "done"
	FailedStatus Status = "failed"
	// PlannedStatus: an AutoStage job stopped after the plan, without a reel: the editor decides on the plan
	// (the trainer's caption, disputed repeats) and replaces the job with its RenderStage (Board.Replace).
	PlannedStatus Status = "planned"
)

// Stage is what a job does.
type Stage string

const (
	// AutoStage transcribes the source and plans the cut; with nothing to decide (no Decide or Hook, no disputed
	// repeats, a cleaned talking head within TargetDuration and its margin) it renders the reel at once, otherwise it stops
	// with PlannedStatus after storing the transcript and the plan. An empty stage is AutoStage.
	AutoStage Stage = "auto"
	// RenderStage renders the reel with the editor's Edit from the stored transcript of the source: the speech
	// is not transcribed again.
	RenderStage Stage = "render"
)

// FailureCode tells the app why a job failed, so it can tell the trainer without technical details.
type FailureCode string

const (
	// NotConfiguredCode: no OPENAI_API_KEY in the worker; nothing is downloaded.
	NotConfiguredCode FailureCode = "not_configured"
	// TranscriptionRejectedCode: the speech recognition API refused the key or the account (401, 403, no quota).
	TranscriptionRejectedCode FailureCode = "transcription_rejected"
	// FontsCode: libass replaced a brand font of the subtitles (SUBTITLES_FONTS_DIR): a configuration error.
	FontsCode FailureCode = "fonts"
	// SourceMissingCode: no source under the key in S3.
	SourceMissingCode FailureCode = "source_missing"
	// SourceTooLargeCode: the source is above MEDIA_WORKER_MAX_SOURCE_SIZE.
	SourceTooLargeCode FailureCode = "source_too_large"
	// SourceTooLongCode: the source is above MEDIA_WORKER_MAX_SOURCE_DURATION (Result.MaxDuration).
	SourceTooLongCode FailureCode = "source_too_long"
	// BadSourceCode: ffprobe cannot read the source or it has no picture.
	BadSourceCode FailureCode = "bad_source"
	// NoAudioCode: the source has no sound; the cut goes by the words.
	NoAudioCode FailureCode = "no_audio"
	// NoSpeechCode: the transcript has no words to keep.
	NoSpeechCode FailureCode = "no_speech"
	// TimeoutCode: the job ran longer than its timeout (EDITOR_JOB_TIMEOUT) from the moment a worker took it.
	TimeoutCode FailureCode = "timeout"
	// QueueTimeoutCode: no worker took the job before its queue deadline (EDITOR_QUEUE_TIMEOUT).
	QueueTimeoutCode FailureCode = "queue_timeout"
	// InvalidEditCode: the plan of a render job cannot be built with its edit (the app checked it, so this is a bug
	// or a change of the transcript): a technical failure, not the trainer's.
	InvalidEditCode FailureCode = "invalid_edit"
	// TemporaryCode: the API, the storage or ffmpeg failed; another attempt may succeed.
	TemporaryCode FailureCode = "temporary"
)

// ErrInvalidJob: a job or a result that cannot be used (an empty ID, a source without a key).
var ErrInvalidJob = errors.New("invalid edit job")

// Job is an edit of the source video of a task; Version is the draft version it makes.
//
// Two limits: a job waits in the queue until QueueDeadline (a few sources in a row wait for each other), and once a
// worker takes it (Board.MarkStarted), it runs for at most Timeout from that moment.
type Job struct {
	ID        string    `json:"id"`
	TaskID    int64     `json:"task_id"`
	Version   int       `json:"version"`
	SourceKey string    `json:"source_key"`
	CreatedAt time.Time `json:"created_at"`
	// QueueDeadline: a job no worker has taken by then is not started.
	QueueDeadline time.Time `json:"queue_deadline"`
	// Timeout of the edit from the moment a worker took the job.
	Timeout time.Duration `json:"timeout"`
	Stage   Stage         `json:"stage,omitempty"`
	// Decide stops an AutoStage job after the plan even without disputed repeats: the trainer's caption is
	// applied by the editor first.
	Decide bool `json:"decide,omitempty"`
	// TargetDuration (seconds) of a talking-head reel: an AutoStage job whose cleaned plan is longer than it with its
	// margin (roughcut.LongerThanTarget) stops after the plan, so the editor picks the best phrases for the reel
	// (EDITOR_REELS_TARGET_DURATION); 0 turns it off.
	TargetDuration float64 `json:"target_duration,omitempty"`
	// Hook stops every AutoStage job after the plan: the editor's model proposes the hook title of the first version
	// (EDITOR_HOOK_TITLE). The worker gets only the checked text of the hook, in the Edit of the RenderStage job.
	Hook bool `json:"hook,omitempty"`
	// Settings and Edit of a RenderStage job: the plan is built with the settings of the plan the editor
	// decided on (so the IDs of its candidates hold) and the editor's changes, checked by the app.
	Settings *roughcut.Settings `json:"settings,omitempty"`
	Edit     *roughcut.Edit     `json:"edit,omitempty"`
	// Summary is the app's own note for the description of the draft; the worker does not read it.
	Summary *Summary `json:"summary,omitempty"`
	// RevisionHash is the app's fingerprint of the trainer's text the job was decided on (the revision; the caption
	// of the first version): a job of a failed revision left on the board is not given to the next revision.
	RevisionHash string `json:"revision_hash,omitempty"`
	// Branding is the trainer's handle over the reel and the end card with it (REELS_HANDLE, REELS_HANDLE_ICON,
	// REELS_END_CARD of the app) and the top line (REELS_TOPBAR_LEFT, REELS_TOPBAR_RIGHT); nil: none. The worker checks
	// them again and draws nothing of an invalid one; the subtitle style decides which of them it draws.
	Branding *Branding `json:"branding,omitempty"`
	// Lease is the token of the local edit (make edit) that waits for the job; empty for the app's jobs. The job is
	// current only while the token holds the lease (Board.Lease, refreshed by the waiting run): the worker drops the
	// job of a killed run within seconds instead of rendering it for nobody.
	Lease string `json:"lease,omitempty"`
}

// Branding is the trainer's Instagram handle drawn over the reel: small at the right edge with the icon (Icon) and
// large on the card over the last seconds (EndCard); and the top line over the reel, a text at the left edge
// («tg: @ana_prokudina») and one at the right edge («2x»).
type Branding struct {
	Handle      string `json:"handle"`
	Icon        bool   `json:"icon,omitempty"`
	EndCard     bool   `json:"end_card,omitempty"`
	TopbarLeft  string `json:"topbar_left,omitempty"`
	TopbarRight string `json:"topbar_right,omitempty"`
}

// Validate checks the handle (when set: «@» and 1–30 small Latin letters, digits, dots and underscores) and the texts
// of the top line; branding without a handle and a top line is invalid (ErrInvalidHandle).
func (b *Branding) Validate() error {
	if b.Handle == "" && b.TopbarLeft == "" && b.TopbarRight == "" {
		return fmt.Errorf("branding has neither a handle nor a top line: %w", ErrInvalidHandle)
	}

	var errs []error

	if b.Handle != "" {
		errs = append(errs, CheckHandle(b.Handle))
	}

	errs = append(errs, CheckTopbar(b.TopbarLeft), CheckTopbar(b.TopbarRight))

	return errors.Join(errs...)
}

// maxTopbarRunes is the longest text of the top line.
const maxTopbarRunes = 40

// ErrInvalidTopbar: a text of the top line that is longer than maxTopbarRunes or has a character that is not printable
// or is markup ({ } \ < >).
var ErrInvalidTopbar = errors.New("invalid top line text")

// CheckTopbar checks a text of the top line (REELS_TOPBAR_LEFT, REELS_TOPBAR_RIGHT): empty (nothing drawn), or at most
// 40 printable characters without { } \ < > (no line breaks, invisible characters or ASS markup reach the document).
func CheckTopbar(text string) error {
	if text == "" {
		return nil
	}

	bad := strings.ContainsFunc(text, func(r rune) bool {
		return !unicode.IsPrint(r) || strings.ContainsRune(`{}\<>`, r)
	})

	if bad || utf8.RuneCountInString(text) > maxTopbarRunes {
		return fmt.Errorf("top line text %q must be 1-%d printable characters without { } \\ < >: %w", text,
			maxTopbarRunes, ErrInvalidTopbar)
	}

	return nil
}

// handleRe is an Instagram handle as the app accepts it (REELS_HANDLE): «@», then 1–30 small letters, digits, dots and
// underscores. The subtitles draw only a handle that matches it, so nothing else reaches the ASS document.
var handleRe = regexp.MustCompile(`^@[a-z0-9._]{1,30}$`)

// ErrInvalidHandle: a handle that is not «@» and 1–30 small Latin letters, digits, dots and underscores.
var ErrInvalidHandle = errors.New("invalid instagram handle")

// CheckHandle checks an Instagram handle («@prokudina.ana»).
func CheckHandle(handle string) error {
	if !handleRe.MatchString(handle) {
		return fmt.Errorf("handle %q must be @ and 1-30 of a-z, 0-9, . and _: %w", handle, ErrInvalidHandle)
	}

	return nil
}

// TextHash is the fingerprint of a text for Job.RevisionHash: sha256 in hex, empty for an empty text.
func TextHash(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}

	sum := sha256.Sum256([]byte(text))

	return hex.EncodeToString(sum[:])
}

// Summary is what the app tells the trainer about a decided version, besides the durations.
type Summary struct {
	// Changes are the sentences about what the editor changed.
	Changes []string `json:"changes,omitempty"`
	// Note is said after them (for example, why the caption was not applied).
	Note string `json:"note,omitempty"`
	// PreviousDuration is the length of the reel of the previous version; 0 for the first one.
	PreviousDuration float64 `json:"previous_duration,omitempty"`
	// Mode of the version when it is a workout (the description says so); empty for a talking head.
	Mode roughcut.Mode `json:"mode,omitempty"`
	// CaptionApplied: the exercise titles were placed by the caption, so the description does not quote it.
	CaptionApplied bool `json:"caption_applied,omitempty"`
	// Selected: the editor picked the best phrases of a talking head for the length of a reel, so the description
	// says the reel was shortened, not only cleaned.
	Selected bool `json:"selected,omitempty"`
}

// EffectiveStage is the stage of the job; empty is AutoStage.
func (j *Job) EffectiveStage() Stage {
	if j.Stage == "" {
		return AutoStage
	}

	return j.Stage
}

// Same reports whether other is this job in the same state: the same stage and creation time. A planned job replaced
// by its render, a render queued again and a job submitted anew after Board.Forget are not the same.
func (j *Job) Same(other *Job) bool {
	return j.EffectiveStage() == other.EffectiveStage() && j.CreatedAt.Equal(other.CreatedAt)
}

// JobID is the ID of the job that makes the version of the task: the same for every run of the task.
func JobID(taskID int64, version int) string {
	return "task-" + strconv.FormatInt(taskID, 10) + "-v" + strconv.Itoa(version)
}

// Validate checks what the worker needs of a job.
func (j *Job) Validate() error {
	switch {
	case j.ID == "" || j.ID != JobID(j.TaskID, j.Version):
		return fmt.Errorf("job id %q does not match task %d version %d: %w", j.ID, j.TaskID, j.Version, ErrInvalidJob)
	case j.TaskID <= 0 || j.Version <= 0:
		return fmt.Errorf("job %q: task and version must be positive: %w", j.ID, ErrInvalidJob)
	case j.SourceKey == "":
		return fmt.Errorf("job %q has no source: %w", j.ID, ErrInvalidJob)
	case j.QueueDeadline.IsZero() || j.Timeout <= 0:
		return fmt.Errorf("job %q has no queue deadline or timeout: %w", j.ID, ErrInvalidJob)
	case j.EffectiveStage() != AutoStage && j.EffectiveStage() != RenderStage:
		return fmt.Errorf("job %q has an unknown stage %q: %w", j.ID, j.Stage, ErrInvalidJob)
	case j.Stage == RenderStage && j.Settings == nil:
		return fmt.Errorf("job %q renders without the plan settings: %w", j.ID, ErrInvalidJob)
	}

	return nil
}

// Result is the outcome of a job. Times are in seconds.
type Result struct {
	JobID  string      `json:"job_id"`
	Status Status      `json:"status"`
	Code   FailureCode `json:"code,omitempty"`
	// SourceDuration of the source and Duration of the reel; MaxDuration is the limit of SourceTooLongCode.
	SourceDuration float64 `json:"source_duration"`
	Duration       float64 `json:"duration,omitempty"`
	MaxDuration    float64 `json:"max_duration,omitempty"`
	// Cuts, Retakes and Candidates count the plan's cut pieces, removed retakes (accepted candidates included) and
	// the disputed repeats left in the reel.
	Cuts       int `json:"cuts,omitempty"`
	Retakes    int `json:"retakes,omitempty"`
	Candidates int `json:"candidates,omitempty"`
	// AudioSeconds is the transcribed audio (Whisper bills by the minute).
	AudioSeconds float64   `json:"audio_seconds,omitempty"`
	FinishedAt   time.Time `json:"finished_at"`
}

// Failed is a failed result of the job with the code.
func Failed(jobID string, code FailureCode, now time.Time) *Result {
	return &Result{JobID: jobID, Status: FailedStatus, Code: code, FinishedAt: now}
}
