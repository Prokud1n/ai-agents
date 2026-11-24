// Package editplan is the editor's (Монтажёр) decision on a cut, made by the model in the app: the trainer's caption
// to the source, a revision of the draft by text, the disputed repeats of the automatic plan (roughcut.Candidate).
//
// The model does not answer with text: it calls the submit_edit tool (SubmitTool) with a structured edit — words to
// cut or bring back, a trim of the start or the end, candidates to cut, corrected subtitle words — or with a refusal
// (cannot, unclear). The edit is checked against the transcript and the current plan (Apply): the indices exist,
// the candidates exist, something is left in the reel, the texts fit; the plan is built with it the same way the
// media worker will build it. Only a checked edit goes to the worker (as roughcut.Edit in the job); the trainer's
// text and the model's answer never do. An invalid edit goes back to the model with the reason once more.
//
// A revision continues the previous version: the new edit is the previous one (roughcut.Plan.Edit) with this change,
// so the revisions add up.
package editplan

import (
	"errors"
	"fmt"

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
}

// IsRevision reports whether the request revises a version.
func (r *Request) IsRevision() bool {
	return r.Version > 1
}

func (r *Request) validate() error {
	if r.Transcript == nil || r.Plan == nil || len(r.Transcript.Words) == 0 {
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
	// NoAnswerOutcome: the model finished without calling submit_edit.
	NoAnswerOutcome OutcomeKind = "no_answer"
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
}
