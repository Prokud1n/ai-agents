package pilot

import (
	"context"
	"time"
)

// Service keeps the feedback, counts the metrics and remembers the reports and the greetings.
type Service struct {
	repo *repo
}

func NewService(r *repo) *Service {
	return &Service{repo: r}
}

// SaveFeedback saves the feedback; a redelivered message returns the saved one with created = false.
func (s *Service) SaveFeedback(ctx context.Context, data *SaveFeedbackData) (Feedback, bool, error) {
	return s.repo.SaveFeedback(ctx, data)
}

// ListFeedback returns the feedback of [since, until), the oldest first, at most limit, and the total.
func (s *Service) ListFeedback(ctx context.Context, since, until time.Time, limit int) ([]Feedback, int, error) {
	return s.repo.ListFeedback(ctx, since, until, limit)
}

// Metrics counts the metrics of the tasks created in [since, until) and the feedback given in it.
func (s *Service) Metrics(ctx context.Context, since, until time.Time) (Metrics, error) {
	rows, err := s.repo.TaskRows(ctx, since, until)
	if err != nil {
		return Metrics{}, err
	}

	feedback, err := s.repo.CountFeedback(ctx, since, until)
	if err != nil {
		return Metrics{}, err
	}

	return Metrics{Since: since, Until: until, Agents: Compute(rows), Feedback: feedback}, nil
}

// ClaimReport marks the report of the week (the date of its scheduled day) as sent; false when it is already.
func (s *Service) ClaimReport(ctx context.Context, week time.Time) (bool, error) {
	return s.repo.ClaimReport(ctx, weekDate(week))
}

// ReleaseReport removes the mark of a report that could not be sent.
func (s *Service) ReleaseReport(ctx context.Context, week time.Time) error {
	return s.repo.ReleaseReport(ctx, weekDate(week))
}

// OnboardingMessage returns the stored greeting of the topic; ErrNotFound if none.
func (s *Service) OnboardingMessage(ctx context.Context, chatID int64, topic string) (OnboardingMessage, error) {
	return s.repo.OnboardingMessage(ctx, chatID, topic)
}

// SaveOnboardingMessage remembers the greeting of the topic.
func (s *Service) SaveOnboardingMessage(ctx context.Context, msg *OnboardingMessage) error {
	return s.repo.SaveOnboardingMessage(ctx, msg)
}

// weekDate is the calendar date of the moment in its own zone, as a UTC midnight for the date column.
func weekDate(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}
