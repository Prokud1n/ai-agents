package tasks

import (
	"fmt"
	"strings"
	"time"
)

const maxErrorRunes = 2000 // last_error is for diagnostics, not a log

// Change is what an updater makes of the locked task: its new state and the rows to change in the same
// transaction.
type Change struct {
	Task     Task
	Version  *Version  // a new draft version
	Feedback *Feedback // the trainer's revision or rejection
	// Redeliver sends the draft of an existing version again (after a failed revision).
	Redeliver *Redelivery
}

// Redelivery resets the delivery of a version: its draft is sent again with the note.
type Redelivery struct {
	Version int
	Note    string
}

// UpdaterFn validates the locked task and returns the change; it never modifies its argument.
// An error cancels the transaction.
type UpdaterFn func(current Task) (Change, error)

// UpdateToRunning starts a run of a queued task whose RunAfter has come; runID identifies the run.
// A running task whose run started more than staleAfter ago is restarted: its worker died (a live run
// ends within the task timeout). If that dead run was the last allowed attempt, the run is given up
// instead (see giveUp): the returned task is then not running.
func UpdateToRunning(now time.Time, staleAfter time.Duration, maxAttempts int, runID string) UpdaterFn {
	return func(t Task) (Change, error) {
		switch {
		case t.Status == QueuedStatus && t.RunAfter.After(now):
			return Change{}, fmt.Errorf("task %d is scheduled for %s: %w", t.ID, t.RunAfter.Format(time.RFC3339), ErrConflict)
		case t.Status == RunningStatus && t.StartedAt != nil && now.Sub(*t.StartedAt) >= staleAfter:
			if t.Attempts >= maxAttempts {
				reason := fmt.Sprintf("the run did not finish within %s, attempts exhausted (%d)", staleAfter, t.Attempts)

				return giveUp(&t, reason, staleRunReason, now), nil
			}
		case t.Status == RunningStatus:
			return Change{}, fmt.Errorf("task %d is already running: %w", t.ID, ErrConflict)
		default:
			if err := checkTransition(&t, RunningStatus); err != nil {
				return Change{}, err
			}
		}

		t.Status = RunningStatus
		t.Attempts++
		t.RunID = runID
		t.StartedAt = new(now)
		t.UpdatedAt = now

		return Change{Task: t}, nil
	}
}

// staleRunReason is the trainer-facing reason when every attempt died without finishing.
const staleRunReason = "Агент не уложился в отведённое время."

// UpdateToAwaitingApproval finishes the run with a new draft version.
func UpdateToAwaitingApproval(runID, content string, now time.Time) UpdaterFn {
	return func(t Task) (Change, error) {
		if err := checkRun(&t, runID, AwaitingApprovalStatus); err != nil {
			return Change{}, err
		}

		t.Version++
		t.Status = AwaitingApprovalStatus
		t.LastError = ""
		t.StartedAt = nil
		t.UpdatedAt = now

		return Change{
			Task:    t,
			Version: &Version{TaskID: t.ID, Version: t.Version, Content: content, CreatedAt: now},
		}, nil
	}
}

// UpdateToRetry puts the task back to the queue after a temporary failure; it runs again after runAfter.
func UpdateToRetry(runID, runErr string, runAfter, now time.Time) UpdaterFn {
	return func(t Task) (Change, error) {
		if err := checkRun(&t, runID, QueuedStatus); err != nil {
			return Change{}, err
		}

		t.Status = QueuedStatus
		t.LastError = truncateError(runErr)
		t.RunAfter = runAfter
		t.EnqueuedAt = now
		t.StartedAt = nil
		t.UpdatedAt = now

		return Change{Task: t}, nil
	}
}

// UpdateToRunFailed gives up the run: attempts are exhausted or the error is permanent (see giveUp).
// runErr is stored for diagnostics, reason is shown to the trainer.
func UpdateToRunFailed(runID, runErr, reason string, now time.Time) UpdaterFn {
	return func(t Task) (Change, error) {
		if err := checkRun(&t, runID, FailedStatus); err != nil {
			return Change{}, err
		}

		return giveUp(&t, runErr, reason, now), nil
	}
}

// giveUp ends a run that will not be retried. A task without a version fails. A task with a version was
// running a revision: it goes back to awaiting approval of the previous version, whose draft is sent again
// with the reason, so the trainer can approve it or revise it again.
func giveUp(current *Task, runErr, reason string, now time.Time) Change {
	t := *current
	t.LastError = truncateError(runErr)
	t.StartedAt = nil
	t.UpdatedAt = now

	if t.Version == 0 {
		t.Status = FailedStatus

		return Change{Task: t}
	}

	t.Status = AwaitingApprovalStatus

	return Change{Task: t, Redeliver: &Redelivery{Version: t.Version, Note: failedRevisionNote(reason)}}
}

// UpdateToInterrupted returns a run interrupted by shutdown to the queue; the attempt is not counted.
func UpdateToInterrupted(runID string, now time.Time) UpdaterFn {
	return func(t Task) (Change, error) {
		if err := checkRun(&t, runID, QueuedStatus); err != nil {
			return Change{}, err
		}

		t.Status = QueuedStatus
		t.Attempts = max(t.Attempts-1, 0)
		t.RunAfter = now
		t.StartedAt = nil
		t.UpdatedAt = now

		return Change{Task: t}, nil
	}
}

// UpdateToApproved approves the draft version.
func UpdateToApproved(version int, now time.Time) UpdaterFn {
	return func(t Task) (Change, error) {
		if err := checkDecision(&t, version, ApprovedStatus); err != nil {
			return Change{}, err
		}

		t.Status = ApprovedStatus
		t.UpdatedAt = now

		return Change{Task: t}, nil
	}
}

// UpdateToRejected rejects the draft version; the reason is optional.
func UpdateToRejected(version int, authorID int64, reason string, now time.Time) UpdaterFn {
	return func(t Task) (Change, error) {
		if err := checkDecision(&t, version, RejectedStatus); err != nil {
			return Change{}, err
		}

		t.Status = RejectedStatus
		t.UpdatedAt = now

		return Change{Task: t, Feedback: &Feedback{
			TaskID: t.ID, Version: version, Kind: RejectFeedbackKind, Text: reason, AuthorID: authorID, CreatedAt: now,
		}}, nil
	}
}

// UpdateToRevisionQueued stores the trainer's revision of the draft version and queues the task in the
// same transaction: the next run makes a new version and has all attempts again.
func UpdateToRevisionQueued(version int, authorID int64, text string, now time.Time) UpdaterFn {
	return func(t Task) (Change, error) {
		if strings.TrimSpace(text) == "" {
			return Change{}, ErrEmptyRevision
		}

		if err := checkDecision(&t, version, QueuedStatus); err != nil {
			return Change{}, err
		}

		t.Status = QueuedStatus
		t.Attempts = 0
		t.LastError = ""
		t.RunAfter = now
		t.EnqueuedAt = now
		t.UpdatedAt = now

		return Change{Task: t, Feedback: &Feedback{
			TaskID: t.ID, Version: version, Kind: ReviseFeedbackKind, Text: text, AuthorID: authorID, CreatedAt: now,
		}}, nil
	}
}

// UpdateToDeliveryFailed fails a task whose draft could not be delivered.
func UpdateToDeliveryFailed(version int, deliveryErr string, now time.Time) UpdaterFn {
	return func(t Task) (Change, error) {
		if t.Status != AwaitingApprovalStatus || t.Version != version {
			return Change{}, fmt.Errorf("task %d version %d is no longer awaiting delivery: %w", t.ID, version, ErrConflict)
		}

		t.Status = FailedStatus
		t.LastError = truncateError(deliveryErr)
		t.UpdatedAt = now

		return Change{Task: t}, nil
	}
}

func checkTransition(t *Task, to Status) error {
	if CanTransition(t.Status, to) {
		return nil
	}

	return fmt.Errorf("task %d cannot go from %s to %s: %w", t.ID, t.Status, to, ErrConflict)
}

// checkRun allows finishing a run only to its own worker.
func checkRun(t *Task, runID string, to Status) error {
	if t.Status != RunningStatus || t.RunID != runID {
		return fmt.Errorf("task %d run %s is no longer current: %w", t.ID, runID, ErrConflict)
	}

	return checkTransition(t, to)
}

// checkDecision allows a decision only on the latest version of a draft awaiting approval.
func checkDecision(t *Task, version int, to Status) error {
	switch {
	case version <= 0 || version > t.Version:
		return fmt.Errorf("task %d version %d: %w", t.ID, version, ErrUnknownVersion)
	case version < t.Version:
		return fmt.Errorf("task %d version %d is outdated, the latest is %d: %w", t.ID, version, t.Version, ErrStaleVersion)
	case t.Status != AwaitingApprovalStatus:
		return fmt.Errorf("task %d version %d is not awaiting approval (status %s): %w", t.ID, version, t.Status, ErrConflict)
	}

	return checkTransition(t, to)
}

func truncateError(s string) string {
	runes := []rune(s)
	if len(runes) <= maxErrorRunes {
		return s
	}

	return string(runes[:maxErrorRunes])
}
