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
	"errors"
	"fmt"
	"strconv"
	"time"
)

// Status of a finished job.
type Status string

const (
	DoneStatus   Status = "done"
	FailedStatus Status = "failed"
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
	// TimeoutCode: the job did not finish by its deadline (EDITOR_JOB_TIMEOUT).
	TimeoutCode FailureCode = "timeout"
	// TemporaryCode: the API, the storage or ffmpeg failed; another attempt may succeed.
	TemporaryCode FailureCode = "temporary"
)

// ErrInvalidJob: a job or a result that cannot be used (an empty ID, a source without a key).
var ErrInvalidJob = errors.New("invalid edit job")

// Job is an edit of the source video of a task; Version is the draft version it makes.
type Job struct {
	ID        string    `json:"id"`
	TaskID    int64     `json:"task_id"`
	Version   int       `json:"version"`
	SourceKey string    `json:"source_key"`
	CreatedAt time.Time `json:"created_at"`
	// Deadline: the worker stops the job by then, the app fails the task after it.
	Deadline time.Time `json:"deadline"`
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
	case j.Deadline.IsZero():
		return fmt.Errorf("job %q has no deadline: %w", j.ID, ErrInvalidJob)
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
	// Cuts, Retakes and Candidates count the plan's cut pieces, removed retakes and the disputed repeats left in
	// the reel (T3.4c decides them).
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
