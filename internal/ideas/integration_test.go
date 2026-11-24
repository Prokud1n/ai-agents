package ideas

import (
	"context"
	"log/slog"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/pgtest"
	"github.com/Prokud1n/ai-agents/internal/providers/postgres"
)

// Integration tests run with INTEGRATION=1 (`make test-integration`): pgtest gives each test its own migrated
// database. Idea tasks are written to the tasks table directly, as the task service leaves them.

const (
	testIdeaText = "челлендж на 7 дней"
	approvedTask = "approved" // tasks.ApprovedStatus
)

type ideaTask struct {
	status  string
	version int
	kind    string
	message int64
	input   string // testIdeaText when empty
}

// insertTask writes a task as the task service would have left it and returns its ID.
func insertTask(t *testing.T, db *postgres.Client, task ideaTask) int64 {
	t.Helper()

	if task.kind == "" {
		task.kind = TaskKind
	}

	if task.input == "" {
		task.input = testIdeaText
	}

	var id int64

	require.NoError(t, db.GetWriteConnection().QueryRow(context.Background(), `
		INSERT INTO tasks (agent, kind, input, source_chat_id, source_message_id, status, version)
		VALUES ($1, $2, $3, -100, $4, $5, $6) RETURNING id`,
		AgentName, task.kind, task.input, task.message, task.status, task.version).Scan(&id))

	return id
}

// storeVersion writes a draft version as the task service stores it after the run.
func storeVersion(t *testing.T, db *postgres.Client, taskID int64, version int, content string) {
	t.Helper()

	_, err := db.GetWriteConnection().Exec(context.Background(),
		`INSERT INTO task_versions (task_id, version, content) VALUES ($1, $2, $3)`, taskID, version, content)
	require.NoError(t, err)
}

func newTestService(db *postgres.Client) *Service {
	return NewService(newRepo(db), slog.New(slog.DiscardHandler))
}

// approvedIdea makes an approved idea task with cards of its versions and saves its idea.
func approvedIdea(t *testing.T, s *Service, db *postgres.Client, message int64, title string) Idea {
	t.Helper()

	ctx := context.Background()
	taskID := insertTask(t, db, ideaTask{status: approvedTask, version: 1, message: message})

	card := validCard()
	card.Title = title
	require.NoError(t, s.SaveCard(ctx, taskID, 1, &card))
	storeVersion(t, db, taskID, 1, FormatCard(&card))

	idea, created, err := s.SaveApproved(ctx, taskID)
	require.NoError(t, err)
	require.True(t, created)

	return idea
}

func TestIntegrationSaveApprovedIdea(t *testing.T) {
	t.Parallel()

	db := pgtest.Connect(t)
	s := newTestService(db)
	ctx := context.Background()

	// A card revised once: version 2 is approved.
	taskID := insertTask(t, db, ideaTask{status: approvedTask, version: 2, message: 1})
	first := validCard()
	first.Title = "Первая версия"
	require.NoError(t, s.SaveCard(ctx, taskID, 1, &first))
	storeVersion(t, db, taskID, 1, FormatCard(&first))

	second := validCard()
	second.Format, second.FormatNote = OtherFormat, testFormatNote
	require.NoError(t, s.SaveCard(ctx, taskID, 2, &first))
	require.NoError(t, s.SaveCard(ctx, taskID, 2, &second)) // a rerun of the version replaces its card
	storeVersion(t, db, taskID, 2, FormatCard(&second))

	// A stale run of a stored version does not replace its card.
	require.ErrorIs(t, s.SaveCard(ctx, taskID, 2, &first), ErrVersionStored)

	card, err := s.GetCard(ctx, taskID, 2)
	require.NoError(t, err)
	assert.Equal(t, second, card)

	idea, created, err := s.SaveApproved(ctx, taskID)
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, second, idea.Card)
	assert.Equal(t, 2, idea.TaskVersion)
	assert.Equal(t, testIdeaText, idea.SourceText)
	assert.False(t, idea.Forwarded)
	assert.Equal(t, NewStatus, idea.Status)

	// A repeated press and concurrent ones: the same idea, no duplicate.
	var wg sync.WaitGroup

	type saved struct {
		idea    Idea
		created bool
		err     error
	}

	results := make([]saved, 4)
	for i := range results {
		wg.Go(func() {
			again, isNew, err := s.SaveApproved(ctx, taskID)
			results[i] = saved{idea: again, created: isNew, err: err}
		})
	}

	wg.Wait()

	for i := range results {
		require.NoError(t, results[i].err)
		assert.False(t, results[i].created)
		assert.Equal(t, idea.ID, results[i].idea.ID)
	}

	list, total, err := s.ListNew(ctx, 10)
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	require.Len(t, list, 1)
	assert.Equal(t, idea.ID, list[0].ID)
}

func TestIntegrationSaveNotApprovedIdea(t *testing.T) {
	t.Parallel()

	db := pgtest.Connect(t)
	s := newTestService(db)
	ctx := context.Background()

	tests := []struct {
		name string
		task ideaTask
		card bool
	}{
		{name: "awaiting approval", task: ideaTask{status: "awaiting_approval", version: 1, message: 1}, card: true},
		{name: "rejected", task: ideaTask{status: "rejected", version: 1, message: 2}, card: true},
		{name: "approved without a card", task: ideaTask{status: approvedTask, version: 1, message: 3}},
		{name: "approved task of another kind", task: ideaTask{status: approvedTask, version: 1, message: 4, kind: "plan"}, card: true},
	}

	for _, tt := range tests {
		taskID := insertTask(t, db, tt.task)
		if tt.card {
			require.NoError(t, s.SaveCard(ctx, taskID, 1, new(validCard())))
		}

		storeVersion(t, db, taskID, 1, FormatCard(new(validCard())))

		_, _, err := s.SaveApproved(ctx, taskID)
		require.ErrorIs(t, err, ErrNotFound, tt.name)
	}

	list, total, err := s.ListNew(ctx, 10)
	require.NoError(t, err)
	assert.Empty(t, list)
	assert.Zero(t, total)
}

// TestIntegrationSyncApproved: an approved idea whose save failed after the approval is restored by the next list.
func TestIntegrationSyncApproved(t *testing.T) {
	t.Parallel()

	db := pgtest.Connect(t)
	s := newTestService(db)
	ctx := context.Background()

	taskID := insertTask(t, db, ideaTask{status: approvedTask, version: 1, message: 1})
	require.NoError(t, s.SaveCard(ctx, taskID, 1, new(validCard())))
	storeVersion(t, db, taskID, 1, FormatCard(new(validCard())))

	list, total, err := s.ListNew(ctx, 10)
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	require.Len(t, list, 1)
	assert.Equal(t, taskID, list[0].TaskID)

	idea, created, err := s.SaveApproved(ctx, taskID)
	require.NoError(t, err)
	assert.False(t, created, "the idea is restored once")
	assert.Equal(t, list[0].ID, idea.ID)
}

// TestIntegrationStaleCardIsNotSaved: a card that is not the approved draft (a stale run of the version wrote it)
// never becomes an idea, neither by the approval nor by the list.
func TestIntegrationStaleCardIsNotSaved(t *testing.T) {
	t.Parallel()

	db := pgtest.Connect(t)
	s := newTestService(db)
	ctx := context.Background()

	taskID := insertTask(t, db, ideaTask{status: approvedTask, version: 1, message: 1})
	stale := validCard()
	stale.Title = "Карточка другого прогона"
	require.NoError(t, s.SaveCard(ctx, taskID, 1, &stale))
	storeVersion(t, db, taskID, 1, FormatCard(new(validCard())))

	_, _, err := s.SaveApproved(ctx, taskID)
	require.ErrorIs(t, err, ErrCardMismatch)

	list, total, err := s.ListNew(ctx, 10)
	require.NoError(t, err)
	assert.Empty(t, list)
	assert.Zero(t, total)
}

// TestIntegrationForwardedIdea: the forwarded mark of the task input is not a part of the words, it is a flag.
func TestIntegrationForwardedIdea(t *testing.T) {
	t.Parallel()

	db := pgtest.Connect(t)
	s := newTestService(db)
	ctx := context.Background()

	taskID := insertTask(t, db, ideaTask{
		status: approvedTask, version: 1, message: 1, input: ForwardedInput("чужая идея про сон"),
	})
	require.NoError(t, s.SaveCard(ctx, taskID, 1, new(validCard())))
	storeVersion(t, db, taskID, 1, FormatCard(new(validCard())))

	idea, created, err := s.SaveApproved(ctx, taskID)
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, "чужая идея про сон", idea.SourceText)
	assert.True(t, idea.Forwarded)
}

func TestIntegrationIdeaStatuses(t *testing.T) {
	t.Parallel()

	db := pgtest.Connect(t)
	s := newTestService(db)
	ctx := context.Background()

	first := approvedIdea(t, s, db, 1, "Первая")
	second := approvedIdea(t, s, db, 2, "Вторая")
	third := approvedIdea(t, s, db, 3, "Третья")
	fourth := approvedIdea(t, s, db, 4, "Четвёртая")

	// /ideas: the newest first, limited, with the total.
	list, total, err := s.ListNew(ctx, 3)
	require.NoError(t, err)
	assert.Equal(t, 4, total)
	assert.Equal(t, []int64{fourth.ID, third.ID, second.ID}, ids(list))

	// The plan: the oldest first.
	forPlan, err := s.ListForPlan(ctx, 2)
	require.NoError(t, err)
	assert.Equal(t, []int64{first.ID, second.ID}, ids(forPlan))

	done, err := s.Close(ctx, second.ID, DoneStatus)
	require.NoError(t, err)
	assert.Equal(t, DoneStatus, done.Status)

	again, err := s.Close(ctx, second.ID, DroppedStatus)
	require.ErrorIs(t, err, ErrClosed)
	assert.Equal(t, DoneStatus, again.Status, "a closed idea comes back as it is")

	_, err = s.Close(ctx, 9999, DoneStatus)
	require.ErrorIs(t, err, ErrNotFound)

	// MarkPlanned: new → planned, closed and unknown ones are skipped, planned ones stay.
	planned, err := s.MarkPlanned(ctx, []int64{first.ID, second.ID, 9999})
	require.NoError(t, err)
	assert.Equal(t, []int64{first.ID}, ids(planned))
	assert.Equal(t, PlannedStatus, planned[0].Status)

	planned, err = s.MarkPlanned(ctx, []int64{first.ID})
	require.NoError(t, err)
	assert.Empty(t, planned)

	list, total, err = s.ListNew(ctx, 10)
	require.NoError(t, err)
	assert.Equal(t, 2, total)
	assert.Equal(t, []int64{fourth.ID, third.ID}, ids(list))

	// A planned idea can be done or dropped.
	dropped, err := s.Close(ctx, first.ID, DroppedStatus)
	require.NoError(t, err)
	assert.Equal(t, DroppedStatus, dropped.Status)
}

func ids(list []Idea) []int64 {
	result := make([]int64, 0, len(list))

	for i := range list {
		result = append(result, list[i].ID)
	}

	return result
}
