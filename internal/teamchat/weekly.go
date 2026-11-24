package teamchat

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/gateway"
	"github.com/Prokud1n/ai-agents/internal/pilot"
)

const (
	daysInWeek = 7
	// reportCatchUp: a report missed at its time (the app was down) is sent on start within this time, later it is
	// skipped: an old week is not news any more.
	reportCatchUp = 24 * time.Hour
	// reportRetryDelay: a report that failed is tried again after it, within reportCatchUp.
	reportRetryDelay = 10 * time.Minute
	// releaseTimeout bounds removing the mark of a report that was not sent.
	releaseTimeout   = 5 * time.Second
	reportTimeLayout = "15:04"
)

var errInvalidSchedule = errors.New("invalid pilot report schedule")

var weekdays = map[string]time.Weekday{
	"sunday": time.Sunday, "monday": time.Monday, "tuesday": time.Tuesday, "wednesday": time.Wednesday,
	"thursday": time.Thursday, "friday": time.Friday, "saturday": time.Saturday,
}

// weeklySchedule is the weekday and time of the report in the team's time zone.
type weeklySchedule struct {
	weekday  time.Weekday
	hour     int
	minute   int
	location *time.Location
}

// parseSchedule reads PILOT_REPORT_WEEKDAY and PILOT_REPORT_TIME; both are needed once either is set.
func parseSchedule(cfg *config.Pilot, location *time.Location) (weeklySchedule, error) {
	weekday, ok := weekdays[strings.ToLower(strings.TrimSpace(cfg.ReportWeekday))]
	if !ok {
		return weeklySchedule{}, fmt.Errorf("PILOT_REPORT_WEEKDAY %q is not an English weekday name: %w",
			cfg.ReportWeekday, errInvalidSchedule)
	}

	at, err := time.Parse(reportTimeLayout, strings.TrimSpace(cfg.ReportTime))
	if err != nil {
		return weeklySchedule{}, fmt.Errorf("PILOT_REPORT_TIME %q is not HH:MM: %w", cfg.ReportTime, errInvalidSchedule)
	}

	return weeklySchedule{weekday: weekday, hour: at.Hour(), minute: at.Minute(), location: location}, nil
}

// lastDue is the latest scheduled moment at or before now.
func (s *weeklySchedule) lastDue(now time.Time) time.Time {
	local := now.In(s.location)
	due := time.Date(local.Year(), local.Month(), local.Day(), s.hour, s.minute, 0, 0, s.location)
	due = due.AddDate(0, 0, -((int(local.Weekday()) - int(s.weekday) + daysInWeek) % daysInWeek))

	if due.After(now) {
		due = due.AddDate(0, 0, -daysInWeek)
	}

	return due
}

// nextDue is the first scheduled moment after now.
func (s *weeklySchedule) nextDue(now time.Time) time.Time {
	return s.lastDue(now).AddDate(0, 0, daysInWeek)
}

// reportStore marks the weeks sent and gives the data of the report.
type reportStore interface {
	ClaimReport(ctx context.Context, week time.Time) (bool, error)
	ReleaseReport(ctx context.Context, week time.Time) error
	Metrics(ctx context.Context, since, until time.Time) (pilot.Metrics, error)
	ListFeedback(ctx context.Context, since, until time.Time, limit int) ([]pilot.Feedback, int, error)
}

// WeeklyReport sends the metrics and the feedback of the week to «Техничка» at PILOT_REPORT_WEEKDAY and
// PILOT_REPORT_TIME (TEAM_TIMEZONE). A week is marked in the database before its report is sent (and unmarked if it
// fails), so a restart or a second app instance never sends it twice. It runs in a goroutine stopped with the app.
type WeeklyReport struct {
	schedule weeklySchedule
	store    reportStore
	tech     techReporter
	log      *slog.Logger
	now      func() time.Time
	cancel   context.CancelFunc
	done     chan struct{}
}

// Start runs the schedule in the background.
func (r *WeeklyReport) Start(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	r.cancel, r.done = cancel, make(chan struct{})

	go r.run(runCtx)

	r.log.InfoContext(ctx, "teamchat: weekly pilot report scheduled", "next", r.schedule.nextDue(r.now()))

	return nil
}

// Stop ends the schedule and waits for a report being sent, at most until ctx is done.
func (r *WeeklyReport) Stop(ctx context.Context) error {
	if r.cancel == nil {
		return nil
	}

	r.cancel()

	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("failed to stop the weekly pilot report: %w", ctx.Err())
	}
}

func (r *WeeklyReport) run(ctx context.Context) {
	defer close(r.done)

	for {
		wake := r.tick(ctx, r.now())

		timer := time.NewTimer(max(wake.Sub(r.now()), 0))

		select {
		case <-ctx.Done():
			timer.Stop()

			return
		case <-timer.C:
		}
	}
}

// tick sends the report of the latest scheduled moment if it is due (not later than reportCatchUp) and returns when to
// check again: the next scheduled moment, or a retry after a failure.
func (r *WeeklyReport) tick(ctx context.Context, now time.Time) time.Time {
	due := r.schedule.lastDue(now)
	next := r.schedule.nextDue(now)

	if now.Sub(due) >= reportCatchUp {
		return next
	}

	err := r.send(ctx, due)
	if err == nil || ctx.Err() != nil {
		return next
	}

	r.log.ErrorContext(ctx, "teamchat: failed to send the weekly pilot report", "week", due, "error", err)

	if retry := now.Add(reportRetryDelay); retry.Sub(due) < reportCatchUp {
		return retry
	}

	return next
}

// send claims the week and sends its report; a week claimed before is skipped.
func (r *WeeklyReport) send(ctx context.Context, due time.Time) error {
	claimed, err := r.store.ClaimReport(ctx, due)
	if err != nil {
		return fmt.Errorf("failed to claim the week: %w", err)
	}

	if !claimed {
		r.log.InfoContext(ctx, "teamchat: weekly pilot report is sent already", "week", due)

		return nil
	}

	if err := r.report(ctx, due); err != nil {
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), releaseTimeout)
		defer cancel()

		if releaseErr := r.store.ReleaseReport(releaseCtx, due); releaseErr != nil {
			r.log.ErrorContext(ctx, "teamchat: failed to release the weekly pilot report", "week", due, "error", releaseErr)
		}

		return err
	}

	r.log.InfoContext(ctx, "teamchat: weekly pilot report sent", "week", due)

	return nil
}

func (r *WeeklyReport) report(ctx context.Context, due time.Time) error {
	since := due.AddDate(0, 0, -daysInWeek)

	metrics, err := r.store.Metrics(ctx, since, due)
	if err != nil {
		return fmt.Errorf("failed to count the metrics: %w", err)
	}

	feedback, total, err := r.store.ListFeedback(ctx, since, due, weeklyFeedbackLimit)
	if err != nil {
		return fmt.Errorf("failed to list the feedback: %w", err)
	}

	text := formatWeeklyReport(&metrics, feedback, total, r.schedule.location)
	if err := r.tech.Report(ctx, string(gateway.MarketerRole), text); err != nil {
		return fmt.Errorf("failed to send the report: %w", err)
	}

	return nil
}
