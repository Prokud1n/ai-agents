package pilot

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/pgtest"
	"github.com/Prokud1n/ai-agents/internal/providers/postgres"
	"github.com/Prokud1n/ai-agents/internal/tasks"
)

// Integration tests run with INTEGRATION=1 (`make test-integration`); tasks are written to the tables directly, as the
// task service leaves them.

const (
	testChat    = int64(-100)
	testSources = "SOURCES"
	testAuthor  = "Анастасия"
)

// insertTask writes a task created at the moment with versions made the given durations after it; returns its ID.
func insertTask(
	t *testing.T,
	db *postgres.Client,
	agent string,
	status tasks.Status,
	created time.Time,
	versions ...time.Duration,
) int64 {
	t.Helper()

	ctx := context.Background()

	var id int64

	require.NoError(t, db.GetWriteConnection().QueryRow(ctx, `
		INSERT INTO tasks (agent, kind, input, source_chat_id, source_message_id, status, version, created_at)
		VALUES ($1, '', 'задача', $2, (SELECT count(*) + 1 FROM tasks), $3, $4, $5) RETURNING id`,
		agent, testChat, string(status), len(versions), created).Scan(&id))

	for i, after := range versions {
		_, err := db.GetWriteConnection().Exec(ctx,
			`INSERT INTO task_versions (task_id, version, content, created_at) VALUES ($1, $2, 'черновик', $3)`,
			id, i+1, created.Add(after))
		require.NoError(t, err)
	}

	return id
}

func TestIntegrationMetrics(t *testing.T) {
	t.Parallel()

	db := pgtest.Connect(t)
	s := NewService(newRepo(db))
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Second)
	since := now.Add(-7 * 24 * time.Hour)
	inPeriod := now.Add(-time.Hour)

	insertTask(t, db, testEditor, tasks.ApprovedStatus, inPeriod, 10*time.Minute)
	insertTask(t, db, testEditor, tasks.ApprovedStatus, inPeriod, 30*time.Minute, time.Hour, 2*time.Hour)
	insertTask(t, db, testEditor, tasks.RejectedStatus, inPeriod, 20*time.Minute)
	insertTask(t, db, testEditor, tasks.QueuedStatus, inPeriod)
	insertTask(t, db, testCopywriter, tasks.ApprovedStatus, inPeriod, time.Minute)
	insertTask(t, db, testEditor, tasks.ApprovedStatus, since.Add(-time.Hour), time.Minute) // before the period

	_, created, err := s.SaveFeedback(ctx, &SaveFeedbackData{
		AuthorID: 42, Text: "Монтаж отличный", Topic: "PRODUCTION", ChatID: testChat, MessageID: 1,
	})
	require.NoError(t, err)
	require.True(t, created)

	metrics, err := s.Metrics(ctx, since, now.Add(time.Minute))
	require.NoError(t, err)

	assert.Equal(t, 1, metrics.Feedback)
	assert.Equal(t, []AgentMetrics{
		{
			Agent: testEditor, Created: 4, Approved: 2, ApprovedFirst: 1, Revisions: 2, Rejected: 1,
			FirstDrafts: 3, MedianFirstDraft: 20 * time.Minute, MaxFirstDraft: 30 * time.Minute,
		},
		{
			Agent: testCopywriter, Created: 1, Approved: 1, ApprovedFirst: 1,
			FirstDrafts: 1, MedianFirstDraft: time.Minute, MaxFirstDraft: time.Minute,
		},
	}, metrics.Agents)
}

func TestIntegrationFeedback(t *testing.T) {
	t.Parallel()

	db := pgtest.Connect(t)
	s := NewService(newRepo(db))
	ctx := context.Background()

	taskID := insertTask(t, db, testEditor, tasks.AwaitingApprovalStatus, time.Now(), time.Minute, 2*time.Minute)
	data := &SaveFeedbackData{
		AuthorID: 42, AuthorName: testAuthor, Text: "Субтитры мелкие", TaskID: taskID, TaskVersion: 2,
		Topic: "PRODUCTION", TopicID: 9, ChatID: testChat, MessageID: 77,
	}

	first, created, err := s.SaveFeedback(ctx, data)
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, taskID, first.TaskID)
	assert.Equal(t, 2, first.TaskVersion)

	again, created, err := s.SaveFeedback(ctx, data)
	require.NoError(t, err)
	assert.False(t, created, "a redelivered message is not saved twice")
	assert.Equal(t, first.ID, again.ID)

	plain, created, err := s.SaveFeedback(ctx, &SaveFeedbackData{AuthorID: 42, Text: "Всё понятно", ChatID: testChat, MessageID: 78})
	require.NoError(t, err)
	assert.True(t, created)
	assert.Zero(t, plain.TaskID)

	list, total, err := s.ListFeedback(ctx, time.Now().Add(-time.Hour), time.Now().Add(time.Hour), 1)
	require.NoError(t, err)
	assert.Equal(t, 2, total)
	require.Len(t, list, 1)
	assert.Equal(t, "Субтитры мелкие", list[0].Text)
	assert.Equal(t, testAuthor, list[0].AuthorName)
}

func TestIntegrationReportsAndGreetings(t *testing.T) {
	t.Parallel()

	db := pgtest.Connect(t)
	s := NewService(newRepo(db))
	ctx := context.Background()

	week := time.Date(2026, 11, 23, 10, 0, 0, 0, time.FixedZone("MSK", 3*60*60))

	claimed, err := s.ClaimReport(ctx, week)
	require.NoError(t, err)
	assert.True(t, claimed)

	claimed, err = s.ClaimReport(ctx, week.Add(time.Hour))
	require.NoError(t, err)
	assert.False(t, claimed, "the week is sent already")

	require.NoError(t, s.ReleaseReport(ctx, week))

	claimed, err = s.ClaimReport(ctx, week)
	require.NoError(t, err)
	assert.True(t, claimed, "a released week is claimed again")

	_, err = s.OnboardingMessage(ctx, testChat, testSources)
	require.ErrorIs(t, err, ErrNotFound)

	require.NoError(t, s.SaveOnboardingMessage(ctx, &OnboardingMessage{ChatID: testChat, Topic: testSources, Role: testEditor, MessageID: 5}))
	require.NoError(t, s.SaveOnboardingMessage(ctx, &OnboardingMessage{ChatID: testChat, Topic: testSources, Role: testEditor, MessageID: 6}))

	msg, err := s.OnboardingMessage(ctx, testChat, testSources)
	require.NoError(t, err)
	assert.Equal(t, OnboardingMessage{ChatID: testChat, Topic: testSources, Role: testEditor, MessageID: 6}, msg)
}
