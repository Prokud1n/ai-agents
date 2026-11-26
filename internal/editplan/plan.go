// Package editplan is the editor's (Монтажёр) decision on a cut, made by the model in the app: the trainer's caption
// to the source, a revision of the draft by text, the disputed repeats of the automatic plan (roughcut.Candidate).
//
// The model does not answer with text: it calls the submit_edit tool (SubmitTool) with a structured edit — words to
// cut or bring back, a trim of the start or the end, candidates to cut, corrected subtitle words — or with a refusal
// (cannot, unclear). The edit is checked against the transcript and the current plan (Apply): the indices exist,
// the candidates exist, something is left in the reel, the texts fit; the plan is built with it the same way the
// media worker will build it. Only a checked edit goes to the worker (as roughcut.Edit in the job): indices, IDs and
// corrected subtitle words, plus the model's short notes that go only into decisions.log; the trainer's text never
// does. An invalid edit goes back to the model with the reason once more.
//
// A revision continues the previous version: the new edit is the previous one (roughcut.Plan.Edit) with this change,
// so the revisions add up.
package editplan

import (
	"errors"
	"fmt"
	"strings"

	"github.com/Prokud1n/ai-agents/internal/roughcut"
	"github.com/Prokud1n/ai-agents/internal/transcription"
)

// AgentName is the editor's agent (agents/editor), the role of the editor's bot.
const AgentName = "editor"

// ErrInvalidRequest: a request without a transcript or a plan.
var ErrInvalidRequest = errors.New("invalid edit request")

// Request is what the editor decides on.
type Request struct {
	TaskID int64
	// Version is the draft version being made: 1 is the first edit, more is a revision.
	Version int
	// Transcript of the source and Plan the decision starts from: the automatic plan of the first version, or the
	// plan of the previous version (with its Edit) for a revision.
	Transcript *transcription.Result
	Plan       *roughcut.Plan
	// Caption is the trainer's caption to the source (the first version), Revision the text of the revision.
	Caption  string
	Revision string
	// TargetSeconds is the longest talking-head reel recommended (EDITOR_REELS_TARGET_DURATION): a first version
	// longer than it after the cleaning is shortened by the editor to TargetSeconds·minTargetShare–TargetSeconds; 0
	// turns the selection off. The trainer's caption or revision may ask for another length (Input.TargetSeconds).
	TargetSeconds float64
	// Hook: the editor proposes the hook title of the first version (EDITOR_HOOK_TITLE). Without it a first version
	// gets one only when the trainer's caption asks; a revision changes it either way.
	Hook bool
	// Cleanup: the editor cleans the first version of a talking head (EDITOR_CLEANUP, Cleaning) of the failed takes,
	// a phrase said again in a row and the preparation for the shooting.
	Cleanup bool
}

// minTargetShare: the recommended reel is from this share of TargetSeconds up to it (30–60 s for 60 s).
const minTargetShare = 0.5

// Selecting reports whether the editor shortens the first version to the recommended length: a talking head that is
// longer than TargetSeconds with its margin after the cleaning (roughcut.LongerThanTarget, the rule the media worker
// stops the job by).
func (r *Request) Selecting() bool {
	return !r.IsRevision() && r.Plan != nil && roughcut.LongerThanTarget(r.Plan.Duration, r.TargetSeconds) &&
		previousEdit(r.Plan).Mode != roughcut.WorkoutMode
}

// Cleaning reports whether the editor cleans this version: the cleanup is on, the first version of a talking head
// that is not shortened by the selection (the selection drops the failed takes itself), with or without a caption.
// The description then counts the cleaning in one line (cleanup_words; without a caption remove_words and the trim
// too).
func (r *Request) Cleaning() bool {
	return !r.IsRevision() && r.Cleanup && r.Plan != nil && !r.Selecting() &&
		previousEdit(r.Plan).Mode != roughcut.WorkoutMode
}

// CleansTakes reports whether the decision may cut the failed takes through cleanup_words: a first talking head
// cleaned (Cleaning) or shortened by the selection (Selecting), which drops the failed takes first. They are cut as
// remove_words, count in the length of the selection and get the line about the cleaning in the description.
func (r *Request) CleansTakes() bool {
	return r.Cleaning() || r.Selecting()
}

// CleanupOnly reports whether the first version is only cleaned: Cleaning without a caption. The model does not
// shorten the reel, cut by time, add exercise titles or mute it, and keeps enough of it (Apply refuses it otherwise);
// without the model the automatic cut is the draft.
func (r *Request) CleanupOnly() bool {
	return r.Cleaning() && strings.TrimSpace(r.Caption) == ""
}

// HookOnly reports whether the first version stopped only for the hook title with the cleanup off: the hook is on, no
// caption, no disputed repeats, a talking head within the recommended length. Then nothing is to be cut: the short
// reel is not cut on the model's own initiative (Apply refuses it), and without the model its automatic cut is the
// draft.
func (r *Request) HookOnly() bool {
	return !r.IsRevision() && !r.Cleanup && r.Hook && strings.TrimSpace(r.Caption) == "" && r.Plan != nil &&
		len(r.Plan.Candidates) == 0 && !r.Selecting() && previousEdit(r.Plan).Mode != roughcut.WorkoutMode
}

// TargetRange is the recommended length of a reel, in seconds: 30–60 s for TargetSeconds 60.
func (r *Request) TargetRange() (low, high float64) {
	return r.TargetSeconds * minTargetShare, r.TargetSeconds
}

// IsRevision reports whether the request revises a version.
func (r *Request) IsRevision() bool {
	return r.Version > 1
}

// validate: a transcript and a plan are needed; the transcript may have no words (a workout shown in silence).
func (r *Request) validate() error {
	if r.Transcript == nil || r.Plan == nil || len(r.Plan.Keep) == 0 {
		return fmt.Errorf("task %d version %d: no transcript or plan: %w", r.TaskID, r.Version, ErrInvalidRequest)
	}

	return nil
}

// OutcomeKind is how the editor decided.
type OutcomeKind string

const (
	// EditOutcome: a checked edit.
	EditOutcome OutcomeKind = "edit"
	// CannotOutcome: the trainer asks for what the editor cannot do (music, B-roll, color, speed...).
	CannotOutcome OutcomeKind = "cannot"
	// UnclearOutcome: the editor did not understand what to change.
	UnclearOutcome OutcomeKind = "unclear"
	// InvalidOutcome: the model's edit failed the checks twice.
	InvalidOutcome OutcomeKind = "invalid"
	// NoAnswerOutcome: the model finished without calling submit_edit, or refused to answer.
	NoAnswerOutcome OutcomeKind = "no_answer"
	// FailedOutcome: the run failed on our side (the answer cut by max_tokens, too many steps, a broken response):
	// a technical failure, not the trainer's.
	FailedOutcome OutcomeKind = "failed"
)

// Outcome is the editor's decision.
type Outcome struct {
	Kind OutcomeKind
	// Edit is the whole edit of the new version (the previous one with this change), checked; EditOutcome only.
	Edit roughcut.Edit
	// Changes are the sentences for the trainer about what changed (EditOutcome).
	Changes []string
	// Cut and Kept count the disputed repeats cut and left by this decision.
	Cut, Kept int
	// Duration of the reel by the new plan (the worker's is exact, this one is before the frame grid).
	Duration float64
	// Reason is the model's explanation or the last check failure: for the log, not for the trainer.
	Reason string
	// Mode of the new version (EditOutcome).
	Mode roughcut.Mode
	// Titles are the exercise titles of the new version with their time in the reel, for the description; Untimed
	// are the exercises the editor could not place in time.
	Titles  []string
	Untimed []string
	// Selected: the editor picked phrases for the length of a reel (the first version longer than the target, or a
	// length the trainer asked for) in a talking head; Target is that length, 0 for the recommended range.
	Selected bool
	Target   float64
	// Cleaned is the number of fragments the cleaning of a first version cut (Request.Cleaning).
	Cleaned int
	// Declined are the texts for the trainer about what was asked and is not done (music, color...): with an edit
	// (the rest is done) or with a refusal (CannotOutcome).
	Declined []string
}
