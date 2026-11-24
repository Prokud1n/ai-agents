// Package ideas is the trainer's idea backlog (T2.4).
//
// /idea <текст> makes a task of the marketer (TaskKind). Executor runs the marketer's agent with the submit_idea tool
// (SubmitTool): the model answers with a card — a title, what the reel or post is about, a format and a hook — that is
// checked (ValidateCard), stored by fields for the version (idea_cards) and sent as a draft whose text is made from the
// fields (FormatCard). When the trainer approves a version («В бэклог»), Service.SaveApproved turns its card into an
// idea with a short number; one idea per task, so a repeated press or a redelivery makes no duplicate.
//
// Statuses: new (in the backlog) → planned (taken into a content plan by /plan) → done | dropped; new may also go
// straight to done or dropped (/idea_done, /idea_drop).
package ideas

import "time"

const (
	// TaskKind is the kind of the marketer's task made by /idea.
	TaskKind = "idea"
	// AgentName is the agent that makes idea cards (agents/marketer).
	AgentName = "marketer"
)

// Status is the state of an idea in the backlog.
type Status string

const (
	NewStatus     Status = "new"
	PlannedStatus Status = "planned"
	DoneStatus    Status = "done"
	DroppedStatus Status = "dropped"
)

// Closed reports whether the idea has left the backlog for good.
func (s Status) Closed() bool {
	return s == DoneStatus || s == DroppedStatus
}

// Format is the proposed form of the content.
type Format string

const (
	TalkFormat     Format = "talk"     // a talking-head reel
	WorkoutFormat  Format = "workout"  // a workout reel: exercises shown
	CarouselFormat Format = "carousel" // a carousel post
	OtherFormat    Format = "other"    // anything else, named in FormatNote
)

// Formats lists the formats in the order of the tool schema.
var Formats = []Format{TalkFormat, WorkoutFormat, CarouselFormat, OtherFormat}

// Card is the marketer's card of an idea.
type Card struct {
	Title   string
	Summary string
	Format  Format
	// FormatNote names the format of OtherFormat; empty for the rest.
	FormatNote string
	Hook       string
}

// Idea is an approved card in the backlog.
type Idea struct {
	// ID is the short number the trainer uses (/idea_done 5).
	ID          int64
	TaskID      int64
	TaskVersion int
	Card        Card
	// SourceText is the words after /idea; Forwarded: they came in a forwarded message (someone else's words).
	SourceText string
	Forwarded  bool
	Status     Status
	CreatedAt  time.Time
	UpdatedAt  time.Time
}
