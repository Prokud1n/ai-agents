package teamchat

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/pilot"
)

// tenOClock is the report time of the tests.
const tenOClock = "10:00"

func TestParseSchedule(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    config.Pilot
		expected weeklySchedule
		fails    bool
	}{
		{
			name:     "monday 10:00",
			input:    config.Pilot{ReportWeekday: "Monday", ReportTime: tenOClock},
			expected: weeklySchedule{weekday: time.Monday, hour: 10, location: moscow},
		},
		{
			name:     "spaces and lower case",
			input:    config.Pilot{ReportWeekday: " sunday ", ReportTime: "21:30"},
			expected: weeklySchedule{weekday: time.Sunday, hour: 21, minute: 30, location: moscow},
		},
		{name: "russian weekday", input: config.Pilot{ReportWeekday: "понедельник", ReportTime: tenOClock}, fails: true},
		{name: "time without the weekday", input: config.Pilot{ReportTime: tenOClock}, fails: true},
		{name: "weekday without the time", input: config.Pilot{ReportWeekday: "monday"}, fails: true},
		{name: "bad time", input: config.Pilot{ReportWeekday: "monday", ReportTime: "25:00"}, fails: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			schedule, err := parseSchedule(&tt.input, moscow)
			if tt.fails {
				require.ErrorIs(t, err, errInvalidSchedule)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expected, schedule)
		})
	}
}

// mondayTen is Monday 10:00 in Moscow (07:00 UTC).
var mondayTen = weeklySchedule{weekday: time.Monday, hour: 10, location: moscow}

func TestScheduleDue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		now          time.Time
		expectedLast time.Time
		expectedNext time.Time
	}{
		{
			name:         "Tuesday: the last is yesterday, the next in six days",
			now:          time.Date(2026, 11, 24, 12, 0, 0, 0, moscow),
			expectedLast: time.Date(2026, 11, 23, 10, 0, 0, 0, moscow),
			expectedNext: time.Date(2026, 11, 30, 10, 0, 0, 0, moscow),
		},
		{
			name:         "Monday before 10:00 in Moscow: the last is a week ago",
			now:          time.Date(2026, 11, 23, 6, 59, 0, 0, time.UTC),
			expectedLast: time.Date(2026, 11, 16, 10, 0, 0, 0, moscow),
			expectedNext: time.Date(2026, 11, 23, 10, 0, 0, 0, moscow),
		},
		{
			name:         "exactly at the time: it is due now",
			now:          time.Date(2026, 11, 23, 7, 0, 0, 0, time.UTC),
			expectedLast: time.Date(2026, 11, 23, 10, 0, 0, 0, moscow),
			expectedNext: time.Date(2026, 11, 30, 10, 0, 0, 0, moscow),
		},
		{
			name:         "Sunday late in UTC is already Monday in Moscow",
			now:          time.Date(2026, 11, 22, 23, 0, 0, 0, time.UTC),
			expectedLast: time.Date(2026, 11, 16, 10, 0, 0, 0, moscow),
			expectedNext: time.Date(2026, 11, 23, 10, 0, 0, 0, moscow),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.True(t, tt.expectedLast.Equal(mondayTen.lastDue(tt.now)), "last: %s", mondayTen.lastDue(tt.now))
			assert.True(t, tt.expectedNext.Equal(mondayTen.nextDue(tt.now)), "next: %s", mondayTen.nextDue(tt.now))
		})
	}
}

// fakeReports is the report store of WeeklyReport.
type fakeReports struct {
	mu sync.Mutex

	claimed  []time.Time
	released []time.Time
	sent     bool // the week is claimed already
	claimErr error
}

func (f *fakeReports) ClaimReport(_ context.Context, week time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.claimed = append(f.claimed, week)

	return !f.sent, f.claimErr
}

func (f *fakeReports) ReleaseReport(_ context.Context, week time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.released = append(f.released, week)

	return nil
}

func (*fakeReports) Metrics(_ context.Context, since, until time.Time) (pilot.Metrics, error) {
	return pilot.Metrics{Since: since, Until: until}, nil
}

func (*fakeReports) ListFeedback(context.Context, time.Time, time.Time, int) ([]pilot.Feedback, int, error) {
	return nil, 0, nil
}

func TestWeeklyReportTick(t *testing.T) {
	t.Parallel()

	due := time.Date(2026, 11, 23, 10, 0, 0, 0, moscow)
	next := due.AddDate(0, 0, 7)
	report := "Сводка пилота за неделю 16.11–23.11 (задачи, созданные за неделю):\n— задач не было.\nОтзывов: 0." +
		"\n\nОтзывов за неделю не было."

	tests := []struct {
		name             string
		now              time.Time
		store            *fakeReports
		techErr          error
		expectedWake     time.Time
		expectedClaimed  []time.Time
		expectedReleased []time.Time
		expectedReports  []string
	}{
		{
			name: "due: claimed and sent", now: due.Add(time.Minute), store: &fakeReports{},
			expectedWake: next, expectedClaimed: []time.Time{due}, expectedReports: []string{report},
		},
		{
			name: "the week is sent already (a restart): not sent again", now: due.Add(time.Hour),
			store:        &fakeReports{sent: true},
			expectedWake: next, expectedClaimed: []time.Time{due},
		},
		{
			name: "missed long ago: skipped", now: due.Add(reportCatchUp), store: &fakeReports{},
			expectedWake: next,
		},
		{
			name: "send failed: released and retried", now: due.Add(time.Minute), store: &fakeReports{},
			techErr:      errTelegramDown,
			expectedWake: due.Add(time.Minute + reportRetryDelay), expectedClaimed: []time.Time{due},
			expectedReleased: []time.Time{due}, expectedReports: []string{report},
		},
		{
			name: "claim failed near the end of the catch-up: the next week", now: due.Add(reportCatchUp - time.Minute),
			store:        &fakeReports{claimErr: errDatabaseDown},
			expectedWake: next, expectedClaimed: []time.Time{due},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tech := &fakeTech{err: tt.techErr}
			r := &WeeklyReport{schedule: mondayTen, store: tt.store, tech: tech, log: slog.New(slog.DiscardHandler)}

			wake := r.tick(t.Context(), tt.now)

			assert.True(t, tt.expectedWake.Equal(wake), "wake: %s", wake)
			assert.Equal(t, unixTimes(tt.expectedClaimed), unixTimes(tt.store.claimed))
			assert.Equal(t, unixTimes(tt.expectedReleased), unixTimes(tt.store.released))
			assert.Equal(t, tt.expectedReports, tech.reports)
		})
	}
}

// unixTimes compares moments regardless of their zones.
func unixTimes(times []time.Time) []int64 {
	result := make([]int64, 0, len(times))
	for _, at := range times {
		result = append(result, at.Unix())
	}

	return result
}

// TestWeeklyReportStops: the goroutine ends with Stop.
func TestWeeklyReportStops(t *testing.T) {
	t.Parallel()

	r := &WeeklyReport{
		schedule: mondayTen, store: &fakeReports{sent: true}, tech: &fakeTech{}, log: slog.New(slog.DiscardHandler),
		now: time.Now,
	}

	require.NoError(t, r.Start(t.Context()))
	require.NoError(t, r.Stop(t.Context()))
}

// TestScheduleDueAcrossDaylightSaving: the report stays at 10:00 local time when the clocks change (Berlin: the last
// Sundays of March and October 2026).
func TestScheduleDueAcrossDaylightSaving(t *testing.T) {
	t.Parallel()

	berlin, err := (&config.Team{Timezone: "Europe/Berlin"}).Location()
	require.NoError(t, err)

	monday := weeklySchedule{weekday: time.Monday, hour: 10, location: berlin}
	sunday := weeklySchedule{weekday: time.Sunday, hour: 10, location: berlin}

	tests := []struct {
		name         string
		schedule     weeklySchedule
		now          time.Time
		expectedLast time.Time // in UTC
		expectedNext time.Time
	}{
		{
			name: "spring: the Sunday the clocks go forward, Monday before is CET, Monday after is CEST", schedule: monday,
			now:          time.Date(2026, 3, 29, 12, 0, 0, 0, berlin),
			expectedLast: time.Date(2026, 3, 23, 9, 0, 0, 0, time.UTC),
			expectedNext: time.Date(2026, 3, 30, 8, 0, 0, 0, time.UTC),
		},
		{
			name: "spring: the report on the day of the change is at 10:00 CEST", schedule: sunday,
			now:          time.Date(2026, 3, 29, 12, 0, 0, 0, berlin),
			expectedLast: time.Date(2026, 3, 29, 8, 0, 0, 0, time.UTC),
			expectedNext: time.Date(2026, 4, 5, 8, 0, 0, 0, time.UTC),
		},
		{
			name: "autumn: the report on the day of the change is at 10:00 CET", schedule: sunday,
			now:          time.Date(2026, 10, 25, 12, 0, 0, 0, berlin),
			expectedLast: time.Date(2026, 10, 25, 9, 0, 0, 0, time.UTC),
			expectedNext: time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC),
		},
		{
			name: "autumn: before the time on the day of the change the last one is a week ago, in CEST", schedule: sunday,
			now:          time.Date(2026, 10, 25, 1, 0, 0, 0, time.UTC),
			expectedLast: time.Date(2026, 10, 18, 8, 0, 0, 0, time.UTC),
			expectedNext: time.Date(2026, 10, 25, 9, 0, 0, 0, time.UTC),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			last, next := tt.schedule.lastDue(tt.now), tt.schedule.nextDue(tt.now)

			assert.True(t, tt.expectedLast.Equal(last), "last: %s", last.UTC())
			assert.True(t, tt.expectedNext.Equal(next), "next: %s", next.UTC())
		})
	}
}
