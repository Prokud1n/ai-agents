package tasks

import (
	"time"

	"github.com/Prokud1n/ai-agents/internal/llmusage"
)

// CreateTaskData is a new task from a message. Source is the idempotency key: the same message
// delivered again returns the existing task.
type CreateTaskData struct {
	Agent    string
	Kind     string
	Input    string
	Media    []string
	Source   Source
	AuthorID int64
}

// ApproveTaskData is a press of «Одобрить» under the draft version.
type ApproveTaskData struct {
	TaskID  int64
	Version int
	UserID  int64
}

// RejectTaskData is a press of «Отклонить»; Reason is optional.
type RejectTaskData struct {
	TaskID  int64
	Version int
	UserID  int64
	Reason  string
}

// RequestRevisionData is the trainer's revision of the draft version.
type RequestRevisionData struct {
	TaskID  int64
	Version int
	UserID  int64
	Text    string
	// SourceMessageID is the message the revision came from (a reply to the draft); 0 if none.
	SourceMessageID int64
}

// RequestRevisionByReplyData is a reply to a draft message: the task and version are found by the
// replied message.
type RequestRevisionByReplyData struct {
	ChatID           int64
	ReplyToMessageID int64
	// MessageID is the reply itself: the same reply again does not revise twice. 0 disables the check.
	MessageID int64
	UserID    int64
	Text      string
}

// StatusReport is the data of /status: tasks by status (all statuses, zeros included) and LLM usage.
type StatusReport struct {
	Since time.Time
	Tasks []StatusCount
	Usage llmusage.Summary
}
