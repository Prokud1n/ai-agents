// Package tasks runs agent tasks and the trainer's approval of their drafts.
//
// A task is created from a message (CreateTaskData, idempotent by the source message), goes through the
// Redis queue to a worker, which runs the agent (Executor) and sends the draft with buttons (Notifier).
// The trainer approves the draft, rejects it, or asks for a revision; a revision runs the agent again
// with the previous version and the trainer's text and gives a new version.
//
// Statuses:
//
//	queued → running → awaiting_approval → approved | rejected
//	           │                │
//	           │                ├→ queued (revision: the next run makes version + 1)
//	           │                └→ failed (the draft could not be delivered)
//	           ├→ queued (temporary error: retry with backoff)
//	           ├→ failed (no version yet: attempts exhausted or a permanent error)
//	           └→ awaiting_approval of the previous version (a revision run failed: the draft is sent again)
//
// PostgreSQL is the source of truth about statuses; every change is made under SELECT ... FOR UPDATE
// by an UpdaterFn. The queue only delivers task ids: a lost or duplicated message does not break a task.
package tasks

import (
	"slices"
	"time"
)

// Status is the state of a task.
type Status string

const (
	QueuedStatus           Status = "queued"
	RunningStatus          Status = "running"
	AwaitingApprovalStatus Status = "awaiting_approval"
	ApprovedStatus         Status = "approved"
	RejectedStatus         Status = "rejected"
	FailedStatus           Status = "failed"
)

// Statuses lists all statuses in the order of the life cycle (the order of /status).
var Statuses = []Status{
	QueuedStatus, RunningStatus, AwaitingApprovalStatus, ApprovedStatus, RejectedStatus, FailedStatus,
}

// transitions are the allowed status changes. A stale running task (its worker died) is restarted by
// UpdateToRunning without a transition: it stays running with a new run id.
var transitions = map[Status][]Status{
	QueuedStatus:           {RunningStatus},
	RunningStatus:          {AwaitingApprovalStatus, QueuedStatus, FailedStatus},
	AwaitingApprovalStatus: {ApprovedStatus, RejectedStatus, QueuedStatus, FailedStatus},
}

// CanTransition reports whether a task may go from one status to another.
func CanTransition(from, to Status) bool {
	return slices.Contains(transitions[from], to)
}

// Final reports whether the task is finished: nothing changes it any more.
func (s Status) Final() bool {
	return s == ApprovedStatus || s == RejectedStatus || s == FailedStatus
}

// FeedbackKind is the trainer's decision that comes with a text.
type FeedbackKind string

const (
	ReviseFeedbackKind FeedbackKind = "revise"
	RejectFeedbackKind FeedbackKind = "reject"
)

// Source is the message a task was created from; (ChatID, MessageID) is the idempotency key.
type Source struct {
	ChatID    int64
	TopicID   int64 // 0: no forum topic; drafts are sent to this topic
	MessageID int64
}

type Task struct {
	ID       int64
	Agent    string
	Kind     string // task type or command, e.g. reels-script
	Input    string
	Media    []string // references to attached media
	Source   Source
	AuthorID int64
	Status   Status
	// Version is the latest draft version; 0 until the first run succeeds.
	Version int
	// Attempts counts runs since the task was created or the last revision was requested.
	Attempts  int
	LastError string
	// RunID identifies the current run: only the worker that started it may finish it.
	RunID string
	// RunAfter: a queued task is not run before it (retry backoff).
	RunAfter time.Time
	// EnqueuedAt is the last time the task was put into the queue; the sweeper queues it again only
	// when this is long ago.
	EnqueuedAt time.Time
	// StartedAt is the start of the current run; nil when the task is not running.
	StartedAt *time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Version is one draft made by the agent.
type Version struct {
	TaskID  int64
	Version int
	Content string
	// DraftMessageID is the Telegram message of the sent draft: nil until it is sent,
	// 0 when the notifier does not send messages.
	DraftMessageID *int64
	CreatedAt      time.Time
}

// Feedback is the trainer's revision request or rejection of a version.
type Feedback struct {
	ID       int64
	TaskID   int64
	Version  int
	Kind     FeedbackKind
	Text     string
	AuthorID int64
	// SourceMessageID is the message the feedback came from (a reply to the draft); 0 if none.
	SourceMessageID int64
	CreatedAt       time.Time
}

// StatusCount is the number of tasks in a status.
type StatusCount struct {
	Status Status
	Count  int64
}
