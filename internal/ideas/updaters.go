package ideas

import (
	"fmt"
	"time"
)

// UpdaterFn changes an idea locked by the repository; an error leaves it as it is.
type UpdaterFn func(current Idea) (Idea, error)

// UpdateToClosed marks the idea done or dropped: from the backlog (new) or from a plan (planned). A closed idea is
// ErrClosed.
func UpdateToClosed(status Status, now time.Time) UpdaterFn {
	return func(i Idea) (Idea, error) {
		if !status.Closed() {
			return Idea{}, fmt.Errorf("idea %d: %q is not a closing status: %w", i.ID, status, ErrValidation)
		}

		if i.Status.Closed() {
			return Idea{}, fmt.Errorf("idea %d is %s: %w", i.ID, i.Status, ErrClosed)
		}

		i.Status = status
		i.UpdatedAt = now

		return i, nil
	}
}

// UpdateToPlanned takes a new idea into a content plan; a planned idea stays as it is, a closed one is ErrClosed.
func UpdateToPlanned(now time.Time) UpdaterFn {
	return func(i Idea) (Idea, error) {
		switch i.Status {
		case PlannedStatus:
			return i, nil
		case NewStatus:
			i.Status = PlannedStatus
			i.UpdatedAt = now

			return i, nil
		default:
			return Idea{}, fmt.Errorf("idea %d is %s: %w", i.ID, i.Status, ErrClosed)
		}
	}
}
