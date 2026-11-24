// Package pilot keeps what the pilot with the trainer needs besides the tasks: her feedback (/feedback), the metrics of
// the pilot counted from the tasks and their versions, the weeks whose report was sent and the pinned greetings of the
// topics (/onboard). The team chat (internal/teamchat) shows all of it.
package pilot

import (
	"errors"
	"time"

	"github.com/Prokud1n/ai-agents/internal/tasks"
)

// ErrNotFound: no such record.
var ErrNotFound = errors.New("pilot record not found")

// Feedback is the trainer's /feedback.
type Feedback struct {
	ID         int64
	AuthorID   int64
	AuthorName string
	Text       string
	// Truncated: the text was cut to the limit of the team chat.
	Truncated bool
	// TaskID and TaskVersion are the draft the feedback replies to; 0 when it is not a reply to a draft.
	TaskID      int64
	TaskVersion int
	// Topic is the gateway topic (PLANNING, SOURCES, …); TopicID its message_thread_id.
	Topic     string
	TopicID   int64
	ChatID    int64
	MessageID int64
	CreatedAt time.Time
}

// OnboardingMessage is the pinned greeting of a topic: the bot (role) that sent it, its message and forum topic, and
// whether it is pinned.
type OnboardingMessage struct {
	ChatID    int64
	Topic     string
	TopicID   int64
	Role      string
	MessageID int64
	Pinned    bool
}

// TaskRow is a task of the period as the metrics need it.
type TaskRow struct {
	Agent   string
	Kind    string
	Status  tasks.Status
	Version int // the latest version: the approved one for an approved task
	// Revisions is how many revisions the trainer asked for (task_feedback of kind revise); a failed revision keeps the
	// version, so the version does not count them.
	Revisions int
	// CreatedAt of the task and of its first version (nil: no draft yet).
	CreatedAt    time.Time
	FirstDraftAt *time.Time
}

// Metrics of a period: tasks created in it by agent, and the feedback given in it.
type Metrics struct {
	Since    time.Time
	Until    time.Time
	Agents   []AgentMetrics
	Feedback int
}
