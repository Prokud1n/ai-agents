package ideas

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// Service keeps the idea backlog: the cards of the idea tasks' versions, the ideas made of approved cards and their
// statuses.
type Service struct {
	repo *repo
	log  *slog.Logger
	now  func() time.Time
}

func NewService(r *repo, log *slog.Logger) *Service {
	return &Service{repo: r, log: log, now: time.Now}
}

// SaveCard stores the card of a task version (Executor).
func (s *Service) SaveCard(ctx context.Context, taskID int64, version int, card *Card) error {
	return s.repo.SaveCard(ctx, taskID, version, card, s.now())
}

// GetCard returns the card of a task version (the previous card of a revision).
func (s *Service) GetCard(ctx context.Context, taskID int64, version int) (Card, error) {
	return s.repo.GetCard(ctx, taskID, version)
}

// SaveApproved puts the card of the approved idea task into the backlog and returns the idea; created is false when
// the task already has its idea (a repeated press, a redelivery): there is one idea per task. A task that is not an
// approved idea task with a card of its latest version is ErrNotFound.
func (s *Service) SaveApproved(ctx context.Context, taskID int64) (Idea, bool, error) {
	existing, err := s.repo.GetByTask(ctx, taskID)
	if err == nil {
		return existing, false, nil
	}

	if !errors.Is(err, ErrNotFound) {
		return Idea{}, false, err
	}

	inserted, err := s.repo.InsertApproved(ctx, taskID, s.now())
	if err != nil {
		return Idea{}, false, err
	}

	idea, err := s.repo.GetByTask(ctx, taskID)
	if errors.Is(err, ErrNotFound) {
		return Idea{}, false, fmt.Errorf("task %d is not an approved idea task with a card: %w", taskID, ErrNotFound)
	}

	if err != nil {
		return Idea{}, false, err
	}

	if inserted {
		s.log.InfoContext(ctx, "ideas: saved to the backlog", "idea_id", idea.ID, "task_id", taskID,
			"version", idea.TaskVersion)
	}

	return idea, inserted, nil
}

// ListNew is /ideas: at most limit ideas of the backlog (new), the newest first, and how many there are in all.
func (s *Service) ListNew(ctx context.Context, limit int) ([]Idea, int, error) {
	if limit <= 0 {
		return nil, 0, fmt.Errorf("idea list limit %d: %w", limit, ErrValidation)
	}

	s.syncApproved(ctx)

	return s.repo.ListByStatus(ctx, NewStatus, limit, true)
}

// ListForPlan returns at most limit ideas of the backlog for a content plan (/plan), the oldest first: the ideas that
// waited longest go first. The plan marks the ideas it took with MarkPlanned.
func (s *Service) ListForPlan(ctx context.Context, limit int) ([]Idea, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("idea list limit %d: %w", limit, ErrValidation)
	}

	s.syncApproved(ctx)

	list, _, err := s.repo.ListByStatus(ctx, NewStatus, limit, false)

	return list, err
}

// MarkPlanned takes the backlog ideas into a content plan (new → planned) and returns the ideas that changed; planned
// ideas stay planned, closed and unknown ones are skipped.
func (s *Service) MarkPlanned(ctx context.Context, ids []int64) ([]Idea, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	changed, err := s.repo.UpdateMany(ctx, ids, UpdateToPlanned(s.now()), func(err error) bool {
		return errors.Is(err, ErrClosed)
	})
	if err != nil {
		return nil, err
	}

	s.log.InfoContext(ctx, "ideas: taken into a plan", "requested", len(ids), "changed", len(changed))

	return changed, nil
}

// Close marks the idea done or dropped (/idea_done, /idea_drop). An unknown number is ErrNotFound; a closed idea is
// ErrClosed and comes back as it is, so the answer can say how it was closed.
func (s *Service) Close(ctx context.Context, id int64, status Status) (Idea, error) {
	idea, err := s.repo.Update(ctx, id, UpdateToClosed(status, s.now()))
	if err != nil {
		return idea, err
	}

	s.log.InfoContext(ctx, "ideas: closed", "idea_id", id, "status", status)

	return idea, nil
}

// syncApproved puts into the backlog the approved ideas whose save after the approval failed; a failure is only
// logged: the list is read anyway.
func (s *Service) syncApproved(ctx context.Context) {
	restored, err := s.repo.SyncApproved(ctx, s.now())
	if err != nil {
		s.log.ErrorContext(ctx, "ideas: failed to sync approved ideas", "error", err)

		return
	}

	if restored > 0 {
		s.log.WarnContext(ctx, "ideas: restored approved ideas missing from the backlog", "count", restored)
	}
}
