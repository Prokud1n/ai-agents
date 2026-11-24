package tasks

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/agents"
	"github.com/Prokud1n/ai-agents/internal/pgtest"
)

// Integration tests run with INTEGRATION=1 (`make test-integration`): pgtest gives each test its own
// migrated database. The worker side is driven by calling Service.Run, as a worker does for a message.

var errTelegramDown = errors.New("telegram is down")

func TestIntegrationCreateIsIdempotent(t *testing.T) {
	t.Parallel()

	env := newTestEnv(t, pgtest.Connect(t))
	ctx := context.Background()

	first, created, err := env.service.Create(ctx, testTaskData(1))
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, QueuedStatus, first.Status)
	assert.Equal(t, []string{"voice/1.ogg"}, first.Media)

	again, created, err := env.service.Create(ctx, testTaskData(1))
	require.NoError(t, err)
	assert.False(t, created, "a redelivered message must not create a task")
	assert.Equal(t, first.ID, again.ID)

	// The key is (chat, message): the same message seen with another topic is the same task.
	otherTopic := testTaskData(1)
	otherTopic.Source.TopicID = 8

	again, created, err = env.service.Create(ctx, otherTopic)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, first.ID, again.ID)

	// Concurrent deliveries of another message: exactly one task.
	const deliveries = 8

	type delivered struct {
		task    Task
		created bool
		err     error
	}

	var (
		wg      sync.WaitGroup
		results = make([]delivered, deliveries)
	)

	for i := range deliveries {
		wg.Go(func() {
			task, isNew, err := env.service.Create(ctx, testTaskData(2))
			results[i] = delivered{task: task, created: isNew, err: err}
		})
	}

	wg.Wait()

	ids := map[int64]bool{}
	creates := 0

	for i := range results {
		require.NoError(t, results[i].err)

		ids[results[i].task.ID] = true
		if results[i].created {
			creates++
		}
	}

	assert.Len(t, ids, 1)
	assert.Equal(t, 1, creates)
	assert.Len(t, env.queue.Enqueued(), 2, "each task is queued once")

	_, _, err = env.service.Create(ctx, &CreateTaskData{Agent: testAgent, Source: Source{ChatID: 1, MessageID: 3}})
	require.ErrorIs(t, err, ErrEmptyTask)
}

func TestIntegrationFullCycle(t *testing.T) {
	t.Parallel()

	env := newTestEnv(t, pgtest.Connect(t),
		fakeResult{text: testVersion1},
		fakeResult{text: testVersion2},
	)
	ctx := context.Background()

	task, _, err := env.service.Create(ctx, testTaskData(10))
	require.NoError(t, err)

	checkFirstVersion(t, env, task.ID)
	checkRevisionByReply(t, env, &task)
	checkSecondVersion(t, env, task.ID)
	checkApproval(t, env, task.ID)
}

// checkFirstVersion: the run makes version 1, it awaits approval and the draft is sent with buttons.
func checkFirstVersion(t *testing.T, env *testEnv, taskID int64) {
	t.Helper()

	ctx := context.Background()

	require.NoError(t, env.service.Run(ctx, taskID))

	task, err := env.service.Get(ctx, taskID)
	require.NoError(t, err)
	assert.Equal(t, AwaitingApprovalStatus, task.Status)
	assert.Equal(t, 1, task.Version)

	drafts := env.notifier.Drafts()
	require.Len(t, drafts, 1)
	assert.Equal(t, testVersion1, drafts[0].Text)
	assert.Empty(t, drafts[0].Note)
	assert.Equal(t, testAgent, drafts[0].Agent)
	assert.Equal(t, testTaskKind, drafts[0].Kind, "the notifier may label the buttons by the kind")
	assert.Equal(t, int64(7), drafts[0].TopicID)
	assert.Equal(t, int64(10), drafts[0].ReplyToMessageID)
	assert.Equal(t, DraftButtons(taskID, 1), drafts[0].Buttons)

	// A duplicate message for a task awaiting approval does not run it again; a sent draft is not resent.
	require.NoError(t, env.service.Run(ctx, taskID))
	require.NoError(t, env.service.Sweep(ctx, true))
	assert.Len(t, env.executor.Prompts(), 1)
	assert.Len(t, env.notifier.Drafts(), 1)
}

// checkRevisionByReply: the trainer replies to the draft of version 1 with a revision.
func checkRevisionByReply(t *testing.T, env *testEnv, task *Task) {
	t.Helper()

	ctx := context.Background()

	found, v1, err := env.service.FindByDraftMessage(ctx, task.Source.ChatID, 1001)
	require.NoError(t, err)
	assert.Equal(t, task.ID, found.ID)
	assert.Equal(t, 1, v1.Version)

	queued, err := env.service.RequestRevisionByReply(ctx, &RequestRevisionByReplyData{
		ChatID: task.Source.ChatID, ReplyToMessageID: 1001, UserID: 555, Text: "Сделай хук короче",
	})
	require.NoError(t, err)
	assert.Equal(t, QueuedStatus, queued.Status, "the revision queues the task at once")
	assert.Equal(t, []int64{task.ID, task.ID}, env.queue.Enqueued(), "queued on create and after the revision")

	env.waitBackground(t) // the draft is closed in the background
	require.Len(t, env.notifier.Closed(), 1)
	assert.Equal(t, ReviseAction, env.notifier.Closed()[0].Decision)

	// The same revision again (a redelivered reply) is refused: version 1 is no longer awaiting approval.
	_, err = env.service.RequestRevision(ctx, &RequestRevisionData{TaskID: task.ID, Version: 1, Text: "ещё раз"})
	require.ErrorIs(t, err, ErrConflict)

	_, err = env.service.RequestRevision(ctx, &RequestRevisionData{TaskID: task.ID, Version: 1, Text: " "})
	require.ErrorIs(t, err, ErrEmptyRevision)
}

// checkSecondVersion: the prompt has the previous version and the revision as data; version 2 is made.
// Nothing goes to the knowledge base before approval.
func checkSecondVersion(t *testing.T, env *testEnv, taskID int64) {
	t.Helper()

	ctx := context.Background()

	require.NoError(t, env.service.Run(ctx, taskID))

	prompts := env.executor.Prompts()
	require.Len(t, prompts, 2)
	assert.Contains(t, prompts[1], "<previous_version number=\"1\">\nВерсия один\n</previous_version>")
	assert.Contains(t, prompts[1], "<trainer_revision>\nСделай хук короче\n</trainer_revision>")

	task, err := env.service.Get(ctx, taskID)
	require.NoError(t, err)
	assert.Equal(t, AwaitingApprovalStatus, task.Status)
	assert.Equal(t, 2, task.Version)

	v2, err := env.service.GetVersion(ctx, taskID, 2)
	require.NoError(t, err)
	assert.Equal(t, testVersion2, v2.Content)
	require.NotNil(t, v2.DraftMessageID)
	assert.Equal(t, int64(1002), *v2.DraftMessageID)

	assert.Empty(t, env.corrections.Saved(), "an unapproved version is not a correction")

	// A reply to the draft of version 1 is a reply to an old draft.
	_, err = env.service.RequestRevisionByReply(ctx, &RequestRevisionByReplyData{
		ChatID: task.Source.ChatID, ReplyToMessageID: 1001, UserID: 555, Text: "ещё",
	})
	require.ErrorIs(t, err, ErrStaleDraft)
	assert.Equal(t, "Ответьте на последний черновик.", UserMessage(err))
}

// checkApproval: buttons of version 1 are stale, version 2 is approved once and becomes a correction.
func checkApproval(t *testing.T, env *testEnv, taskID int64) {
	t.Helper()

	ctx := context.Background()

	_, err := env.service.Approve(ctx, &ApproveTaskData{TaskID: taskID, Version: 1, UserID: 555})
	require.ErrorIs(t, err, ErrStaleVersion)

	task, err := env.service.Approve(ctx, &ApproveTaskData{TaskID: taskID, Version: 2, UserID: 555})
	require.NoError(t, err)
	assert.Equal(t, ApprovedStatus, task.Status)

	env.waitBackground(t) // the draft is closed and the correction saved in the background

	closed := env.notifier.Closed()
	require.Len(t, closed, 2)
	assert.Equal(t, CloseDraftData{
		TaskID: taskID, Version: 2, Agent: testAgent, Kind: testTaskKind, ChatID: task.Source.ChatID,
		TopicID: task.Source.TopicID, MessageID: 1002, Decision: ApproveAction,
	}, closed[1])

	saved := env.corrections.Saved()
	require.Len(t, saved, 1)
	assert.Equal(t, testAgent, saved[0].Agent)
	assert.Equal(t, testTaskKind, saved[0].TaskKind)
	assert.Equal(t, testVersion1, saved[0].Original)
	assert.Equal(t, testVersion2, saved[0].Corrected)
	assert.Equal(t, "Сделай хук короче", saved[0].Comment)

	// A repeated press changes nothing.
	_, err = env.service.Approve(ctx, &ApproveTaskData{TaskID: taskID, Version: 2, UserID: 555})
	require.ErrorIs(t, err, ErrConflict)
	_, err = env.service.Reject(ctx, &RejectTaskData{TaskID: taskID, Version: 2, UserID: 555})
	require.ErrorIs(t, err, ErrConflict)
	assert.Len(t, env.corrections.Saved(), 1)

	task, err = env.service.Get(ctx, taskID)
	require.NoError(t, err)
	assert.Equal(t, ApprovedStatus, task.Status)
}

// TestIntegrationFailedRevisionReturnsPreviousVersion: when a revision cannot be applied, the task is not
// stuck: the previous draft comes again with buttons and the reason, and can be approved or revised again.
func TestIntegrationFailedRevisionReturnsPreviousVersion(t *testing.T) {
	t.Parallel()

	env := newTestEnv(t, pgtest.Connect(t),
		fakeResult{text: testVersion1},
		fakeResult{err: ClassifyAgentError(errors.Join(errors.New("stop_reason refusal, request_id req_9"), agents.ErrRefusal))},
		fakeResult{text: testVersion2},
	)
	ctx := context.Background()

	task, _, err := env.service.Create(ctx, testTaskData(15))
	require.NoError(t, err)
	require.NoError(t, env.service.Run(ctx, task.ID))

	_, err = env.service.RequestRevision(ctx, &RequestRevisionData{TaskID: task.ID, Version: 1, Text: "Добавь рецепт"})
	require.NoError(t, err)
	require.NoError(t, env.service.Run(ctx, task.ID))

	task, err = env.service.Get(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, AwaitingApprovalStatus, task.Status)
	assert.Equal(t, 1, task.Version)
	assert.Contains(t, task.LastError, "req_9", "the details stay in last_error")

	drafts := env.notifier.Drafts()
	require.Len(t, drafts, 2)
	assert.Equal(t, 1, drafts[1].Version)
	assert.Equal(t, testVersion1, drafts[1].Text)
	assert.Equal(t, DraftButtons(task.ID, 1), drafts[1].Buttons)
	assert.Equal(t, "Не получилось применить правку. Агент отказался выполнять задачу. Переформулируйте запрос. "+
		"Можно одобрить эту версию или дать правку ещё раз.", drafts[1].Note)
	assert.NotContains(t, drafts[1].Note, "req_9")
	assert.Empty(t, env.notifier.Failures())

	// The first message of version 1 was replaced by the resent draft: a reply to it is a stale reply.
	_, _, err = env.service.FindByDraftMessage(ctx, task.Source.ChatID, 1001)
	require.ErrorIs(t, err, ErrStaleDraft)

	// The new draft message is the one to reply to; a second revision works.
	found, v, err := env.service.FindByDraftMessage(ctx, task.Source.ChatID, 1002)
	require.NoError(t, err)
	assert.Equal(t, task.ID, found.ID)
	assert.Equal(t, 1, v.Version)

	_, err = env.service.RequestRevisionByReply(ctx, &RequestRevisionByReplyData{
		ChatID: task.Source.ChatID, ReplyToMessageID: 1002, UserID: 555, Text: "Добавь рецепт, коротко",
	})
	require.NoError(t, err)
	require.NoError(t, env.service.Run(ctx, task.ID))

	task, err = env.service.Get(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, AwaitingApprovalStatus, task.Status)
	assert.Equal(t, 2, task.Version)

	_, err = env.service.Approve(ctx, &ApproveTaskData{TaskID: task.ID, Version: 2})
	require.NoError(t, err)
	env.waitBackground(t)

	saved := env.corrections.Saved()
	require.Len(t, saved, 1)
	assert.Equal(t, "Добавь рецепт, коротко", saved[0].Comment, "the revision that produced the approved version")
}

func TestIntegrationReject(t *testing.T) {
	t.Parallel()

	env := newTestEnv(t, pgtest.Connect(t))
	ctx := context.Background()

	task, _, err := env.service.Create(ctx, testTaskData(20))
	require.NoError(t, err)
	require.NoError(t, env.service.Run(ctx, task.ID))

	task, err = env.service.Reject(ctx, &RejectTaskData{TaskID: task.ID, Version: 1, UserID: 555, Reason: "не та тема"})
	require.NoError(t, err)
	assert.Equal(t, RejectedStatus, task.Status)

	feedback, err := env.service.repo.LatestFeedback(ctx, task.ID, 1, RejectFeedbackKind)
	require.NoError(t, err)
	assert.Equal(t, "не та тема", feedback.Text)
	assert.Equal(t, int64(555), feedback.AuthorID)

	_, err = env.service.RequestRevision(ctx, &RequestRevisionData{TaskID: task.ID, Version: 1, Text: "передумал"})
	require.ErrorIs(t, err, ErrConflict)

	_, err = env.service.Approve(ctx, &ApproveTaskData{TaskID: task.ID + 100, Version: 1})
	require.ErrorIs(t, err, ErrNotFound)

	_, _, err = env.service.FindByDraftMessage(ctx, task.Source.ChatID, 999)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestIntegrationRetriesThenFailed(t *testing.T) {
	t.Parallel()

	env := newTestEnv(t, pgtest.Connect(t), fakeResult{err: errTemporary})
	ctx := context.Background()

	task, _, err := env.service.Create(ctx, testTaskData(30))
	require.NoError(t, err)

	for attempt := 1; attempt < 3; attempt++ {
		require.NoError(t, env.service.Run(ctx, task.ID))

		task, err = env.service.Get(ctx, task.ID)
		require.NoError(t, err)
		assert.Equal(t, QueuedStatus, task.Status)
		assert.Equal(t, attempt, task.Attempts)
		assert.Contains(t, task.LastError, errTemporary.Error())

		at, ok := env.queue.Scheduled(task.ID)
		require.True(t, ok)
		assert.Equal(t, env.clock.Now().Add(Backoff(attempt, 30*time.Second, 10*time.Minute)), at)
		assert.WithinDuration(t, at, task.RunAfter, 0)

		// Not due yet: the message delivered early is skipped.
		require.NoError(t, env.service.Run(ctx, task.ID))
		assert.Len(t, env.executor.Prompts(), attempt)

		env.clock.Advance(at.Sub(env.clock.Now()))
	}

	require.NoError(t, env.service.Run(ctx, task.ID))

	task, err = env.service.Get(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, FailedStatus, task.Status)
	assert.Equal(t, 3, task.Attempts)
	assert.Contains(t, task.LastError, errTemporary.Error())

	failures := env.notifier.Failures()
	require.Len(t, failures, 1)
	assert.Equal(t, "Не получилось выполнить задачу. Сервис ИИ временно недоступен: попыток было 3. Попробуйте позже.",
		failures[0].Message)
	assert.NotContains(t, failures[0].Message, errTemporary.Error(), "internal errors are not shown to the trainer")
	assert.Equal(t, int64(30), failures[0].ReplyToMessageID)

	require.NoError(t, env.service.Run(ctx, task.ID), "a failed task is not run again")
	assert.Len(t, env.executor.Prompts(), 3)

	// The executor knows the run whose failure gives the task up (the editor leaves no work behind for it).
	runs := env.executor.Data()
	require.Len(t, runs, 3)
	assert.Equal(t, []bool{false, false, true}, []bool{runs[0].LastAttempt, runs[1].LastAttempt, runs[2].LastAttempt})
}

func TestIntegrationPermanentErrorFailsAtOnce(t *testing.T) {
	t.Parallel()

	refusal := ClassifyAgentError(agents.ErrRefusal)
	env := newTestEnv(t, pgtest.Connect(t), fakeResult{err: refusal})
	ctx := context.Background()

	task, _, err := env.service.Create(ctx, testTaskData(40))
	require.NoError(t, err)
	require.NoError(t, env.service.Run(ctx, task.ID))

	task, err = env.service.Get(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, FailedStatus, task.Status)
	assert.Equal(t, 1, task.Attempts)

	require.Len(t, env.notifier.Failures(), 1)
	assert.Equal(t, "Не получилось выполнить задачу. Агент отказался выполнять задачу. Переформулируйте запрос.",
		env.notifier.Failures()[0].Message)
}

func TestIntegrationPanickingExecutorIsRetried(t *testing.T) {
	t.Parallel()

	env := newTestEnv(t, pgtest.Connect(t), fakeResult{panicWith: "nil pointer in a tool"}, fakeResult{text: "готово"})
	ctx := context.Background()

	task, _, err := env.service.Create(ctx, testTaskData(45))
	require.NoError(t, err)

	var runErr error

	require.NotPanics(t, func() { runErr = env.service.Run(ctx, task.ID) })
	require.NoError(t, runErr)

	task, err = env.service.Get(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, QueuedStatus, task.Status, "a panic is retried like a temporary error")
	assert.Contains(t, task.LastError, "executor panicked")

	env.clock.Advance(time.Hour)
	require.NoError(t, env.service.Run(ctx, task.ID))

	task, err = env.service.Get(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, AwaitingApprovalStatus, task.Status)
}

func TestIntegrationStopKeepsRunOutcome(t *testing.T) {
	t.Parallel()

	env := newTestEnv(t, pgtest.Connect(t), fakeResult{err: context.Canceled})
	ctx := context.Background()

	task, _, err := env.service.Create(ctx, testTaskData(50))
	require.NoError(t, err)

	// context.Canceled from the executor while the worker's context is alive is an ordinary failure.
	require.NoError(t, env.service.Run(ctx, task.ID))

	task, err = env.service.Get(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, QueuedStatus, task.Status, "a run whose own context is alive retries")
	assert.Equal(t, 1, task.Attempts)

	// The worker's context is cancelled mid-run (stop): the attempt is not counted.
	env.clock.Advance(time.Hour)

	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()

	env.executor.SetResults(cancelRun, fakeResult{err: context.Canceled})

	require.NoError(t, env.service.Run(runCtx, task.ID))

	task, err = env.service.Get(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, QueuedStatus, task.Status)
	assert.Equal(t, 1, task.Attempts, "the interrupted attempt is not counted")
	assert.Empty(t, env.notifier.Failures())

	// Stop comes right after the agent answered: the answer is stored and sent anyway.
	stopCtx, stop := context.WithCancel(ctx)
	defer stop()

	env.executor.SetResults(stop, fakeResult{text: "готово до остановки"})

	require.NoError(t, env.service.Run(stopCtx, task.ID))

	task, err = env.service.Get(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, AwaitingApprovalStatus, task.Status)
	require.Len(t, env.notifier.Drafts(), 1)
	assert.Equal(t, "готово до остановки", env.notifier.Drafts()[0].Text)
}

func TestIntegrationStaleRunOutOfAttemptsFails(t *testing.T) {
	t.Parallel()

	env := newTestEnv(t, pgtest.Connect(t))
	ctx := context.Background()

	task, _, err := env.service.Create(ctx, testTaskData(55))
	require.NoError(t, err)

	// Every attempt died: the last one is still marked running.
	_, err = env.service.repo.Update(ctx, task.ID, func(cur Task) (Change, error) {
		cur.Status, cur.Attempts, cur.RunID, cur.StartedAt = RunningStatus, testMaxAttempts, "dead", new(testNow)

		return Change{Task: cur}, nil
	})
	require.NoError(t, err)

	env.clock.Advance(env.service.staleAfter())
	require.NoError(t, env.service.Sweep(ctx, false))
	assert.Equal(t, 1, env.queue.Schedules(task.ID), "the sweeper queues the dead run")

	require.NoError(t, env.service.Run(ctx, task.ID))

	task, err = env.service.Get(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, FailedStatus, task.Status, "not restarted forever")
	assert.Empty(t, env.executor.Prompts())
	require.Len(t, env.notifier.Failures(), 1)
	assert.Equal(t, "Не получилось выполнить задачу. Агент не уложился в отведённое время.", env.notifier.Failures()[0].Message)
}

func TestIntegrationDraftDelivery(t *testing.T) {
	t.Parallel()

	env := newTestEnv(t, pgtest.Connect(t))
	ctx := context.Background()

	env.notifier.SetSendErr(errTelegramDown)

	task, _, err := env.service.Create(ctx, testTaskData(60))
	require.NoError(t, err)
	require.NoError(t, env.service.Run(ctx, task.ID))
	assert.Equal(t, 1, env.notifier.SendCalls())

	// Not due yet: the sweeper waits for the backoff.
	require.NoError(t, env.service.Sweep(ctx, false))
	assert.Equal(t, 1, env.notifier.SendCalls())

	env.clock.Advance(30 * time.Second)
	require.NoError(t, env.service.Sweep(ctx, false))
	assert.Equal(t, 2, env.notifier.SendCalls())

	env.clock.Advance(time.Minute)
	require.NoError(t, env.service.Sweep(ctx, false))
	assert.Equal(t, 3, env.notifier.SendCalls())

	task, err = env.service.Get(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, FailedStatus, task.Status, "the draft is not resent forever")
	assert.Contains(t, task.LastError, errTelegramDown.Error())
	require.Len(t, env.notifier.Failures(), 1)
	assert.Equal(t, "Не получилось выполнить задачу. Черновик не удалось отправить.", env.notifier.Failures()[0].Message)

	env.clock.Advance(time.Hour)
	require.NoError(t, env.service.Sweep(ctx, false))
	assert.Equal(t, 3, env.notifier.SendCalls())
}

// TestIntegrationDraftDeliveryIsClaimedOnce: concurrent senders (workers, sweepers) send a draft once.
func TestIntegrationDraftDeliveryIsClaimedOnce(t *testing.T) {
	t.Parallel()

	env := newTestEnv(t, pgtest.Connect(t))
	ctx := context.Background()

	env.notifier.SetSendErr(errTelegramDown)

	task, _, err := env.service.Create(ctx, testTaskData(65))
	require.NoError(t, err)
	require.NoError(t, env.service.Run(ctx, task.ID))

	env.notifier.SetSendErr(nil)
	env.clock.Advance(time.Hour)

	const senders = 8

	var (
		wg       sync.WaitGroup
		sweepErr error
	)

	for range senders {
		wg.Go(func() { env.service.deliver(ctx, task.ID, 1) })
	}

	wg.Go(func() { sweepErr = env.service.Sweep(ctx, false) })
	wg.Wait()

	require.NoError(t, sweepErr)
	assert.Len(t, env.notifier.Drafts(), 1)

	v1, err := env.service.GetVersion(ctx, task.ID, 1)
	require.NoError(t, err)
	require.NotNil(t, v1.DraftMessageID)
	assert.Equal(t, int64(1001), *v1.DraftMessageID)
}

func TestIntegrationSweep(t *testing.T) {
	t.Parallel()

	env := newTestEnv(t, pgtest.Connect(t))
	ctx := context.Background()

	// Redis is down while the task is created: it is only in PostgreSQL.
	env.queue.SetErr(errors.New("redis is down"))

	lost, created, err := env.service.Create(ctx, testTaskData(70))
	require.NoError(t, err)
	assert.True(t, created)

	env.queue.SetErr(nil)

	require.NoError(t, env.service.Sweep(ctx, false))
	assert.Zero(t, env.queue.Schedules(lost.ID), "a fresh queued task is not swept: it may still be in the queue")

	require.NoError(t, env.service.Sweep(ctx, true))
	assert.Equal(t, 1, env.queue.Schedules(lost.ID), "the startup sweep queues every due task")

	// Still not taken (the queue is busy): later sweeps do not queue it again every time.
	env.clock.Advance(time.Minute)
	require.NoError(t, env.service.Sweep(ctx, false))
	assert.Equal(t, 1, env.queue.Schedules(lost.ID))

	env.clock.Advance(env.service.staleAfter())
	require.NoError(t, env.service.Sweep(ctx, false))
	assert.Equal(t, 2, env.queue.Schedules(lost.ID), "queued again once it looks lost")

	// A run whose worker died is restarted once it is stale.
	stuck, _, err := env.service.Create(ctx, testTaskData(71))
	require.NoError(t, err)

	_, err = env.service.repo.Update(ctx, stuck.ID,
		UpdateToRunning(env.clock.Now(), env.service.staleAfter(), testMaxAttempts, "dead"))
	require.NoError(t, err)

	env.clock.Advance(env.service.staleAfter())
	require.NoError(t, env.service.Sweep(ctx, false))
	assert.Equal(t, 1, env.queue.Schedules(stuck.ID))

	require.NoError(t, env.service.Run(ctx, stuck.ID))

	stuck, err = env.service.Get(ctx, stuck.ID)
	require.NoError(t, err)
	assert.Equal(t, AwaitingApprovalStatus, stuck.Status)
	assert.Equal(t, 2, stuck.Attempts)
}

func TestIntegrationStatusReport(t *testing.T) {
	t.Parallel()

	env := newTestEnv(t, pgtest.Connect(t))
	ctx := context.Background()

	for i := range int64(3) {
		_, _, err := env.service.Create(ctx, testTaskData(80+i))
		require.NoError(t, err)
	}

	approved, _, err := env.service.Create(ctx, testTaskData(90))
	require.NoError(t, err)
	require.NoError(t, env.service.Run(ctx, approved.ID))
	_, err = env.service.Approve(ctx, &ApproveTaskData{TaskID: approved.ID, Version: 1})
	require.NoError(t, err)

	since := testNow.Add(-24 * time.Hour)

	report, err := env.service.StatusReport(ctx, since)
	require.NoError(t, err)
	assert.Equal(t, since, report.Since)
	assert.Equal(t, since, report.Usage.Since)
	assert.Equal(t, int64(4), report.Usage.Total.Calls)
	require.Len(t, report.Tasks, len(Statuses))
	assert.Equal(t, StatusCount{Status: QueuedStatus, Count: 3}, report.Tasks[0])
	assert.Equal(t, StatusCount{Status: ApprovedStatus, Count: 1}, report.Tasks[3])
}

// TestIntegrationDuplicateDraftIsClosed: if another sender stored its message first (the lease expired
// while this one was sending), this message is a duplicate: its buttons are removed and a reply to it
// is answered as a reply to an old draft.
func TestIntegrationDuplicateDraftIsClosed(t *testing.T) {
	t.Parallel()

	env := newTestEnv(t, pgtest.Connect(t))
	ctx := context.Background()

	task, _, err := env.service.Create(ctx, testTaskData(75))
	require.NoError(t, err)

	env.notifier.OnSend(func() {
		_, err := env.service.repo.SetDraftMessages(ctx, task.ID, 1, &SentDraft{MessageID: 777})
		require.NoError(t, err)
	})

	require.NoError(t, env.service.Run(ctx, task.ID))

	v1, err := env.service.GetVersion(ctx, task.ID, 1)
	require.NoError(t, err)
	require.NotNil(t, v1.DraftMessageID)
	assert.Equal(t, int64(777), *v1.DraftMessageID, "the first stored message stays the draft")

	closed := env.notifier.Closed()
	require.Len(t, closed, 1)
	assert.Equal(t, CloseDraftData{
		TaskID: task.ID, Version: 1, Agent: testAgent, Kind: testTaskKind, ChatID: task.Source.ChatID,
		TopicID: task.Source.TopicID, MessageID: 1001, Duplicate: true,
	}, closed[0])

	_, _, err = env.service.FindByDraftMessage(ctx, task.Source.ChatID, 1001)
	require.ErrorIs(t, err, ErrStaleDraft)

	found, _, err := env.service.FindByDraftMessage(ctx, task.Source.ChatID, 777)
	require.NoError(t, err)
	assert.Equal(t, task.ID, found.ID)
}

// TestIntegrationRevisionByReplyToPart: a reply to an earlier part of a long draft is a revision, and the
// same reply delivered again does not revise twice.
func TestIntegrationRevisionByReplyToPart(t *testing.T) {
	t.Parallel()

	env := newTestEnv(t, pgtest.Connect(t), fakeResult{text: testVersion1}, fakeResult{text: testVersion2})
	env.notifier.parts = true
	ctx := context.Background()

	task, _, err := env.service.Create(ctx, testTaskData(80))
	require.NoError(t, err)
	require.NoError(t, env.service.Run(ctx, task.ID))

	firstPart := int64(1001 + partIDOffset)

	found, v1, err := env.service.FindByDraftMessage(ctx, task.Source.ChatID, firstPart)
	require.NoError(t, err)
	assert.Equal(t, task.ID, found.ID)
	assert.Equal(t, 1, v1.Version)

	reply := &RequestRevisionByReplyData{
		ChatID: task.Source.ChatID, ReplyToMessageID: firstPart, MessageID: 81, UserID: 555, Text: "Короче",
	}

	queued, err := env.service.RequestRevisionByReply(ctx, reply)
	require.NoError(t, err)
	assert.Equal(t, QueuedStatus, queued.Status)

	again, err := env.service.RequestRevisionByReply(ctx, reply)
	require.NoError(t, err, "a redelivered reply is not an error")
	assert.Equal(t, QueuedStatus, again.Status)
	assert.Len(t, env.queue.Enqueued(), 2, "queued on create and once for the revision")

	other := *reply
	other.MessageID = 82

	_, err = env.service.RequestRevisionByReply(ctx, &other)
	require.ErrorIs(t, err, ErrConflict, "another reply while the new version is being made")

	require.NoError(t, env.service.Run(ctx, task.ID))

	_, err = env.service.RequestRevisionByReply(ctx, &RequestRevisionByReplyData{
		ChatID: task.Source.ChatID, ReplyToMessageID: firstPart, MessageID: 83, UserID: 555, Text: "ещё",
	})
	require.ErrorIs(t, err, ErrStaleDraft, "a part of version 1 is an old draft once version 2 is sent")

	found, v2, err := env.service.FindByDraftMessage(ctx, task.Source.ChatID, 1002+partIDOffset)
	require.NoError(t, err)
	assert.Equal(t, task.ID, found.ID)
	assert.Equal(t, 2, v2.Version)

	env.waitBackground(t)
}

// TestIntegrationDraftBeingSent: parts of a draft whose send failed become old messages of the version; while
// the draft is not sent, a reply to a message of the agent's bot is ErrDraftNotReady, not a new task.
func TestIntegrationDraftBeingSent(t *testing.T) {
	t.Parallel()

	env := newTestEnv(t, pgtest.Connect(t), fakeResult{text: testVersion1})
	env.notifier.partialOnErr = []int64{3001}
	env.notifier.SetSendErr(errTelegramDown)
	ctx := context.Background()

	task, _, err := env.service.Create(ctx, testTaskData(85))
	require.NoError(t, err)
	require.NoError(t, env.service.Run(ctx, task.ID))

	running, err := env.service.Get(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, AwaitingApprovalStatus, running.Status, "the draft waits for the next delivery attempt")

	_, _, err = env.service.FindByDraftMessage(ctx, task.Source.ChatID, 3001)
	require.ErrorIs(t, err, ErrStaleDraft, "a part of the failed send is an old message")

	reply := &RequestRevisionByReplyData{
		ChatID: task.Source.ChatID, ReplyToMessageID: 3002, MessageID: 86, UserID: 555, Text: "Короче",
		TopicID: task.Source.TopicID, Agent: testAgent,
	}

	_, err = env.service.RequestRevisionByReply(ctx, reply)
	require.ErrorIs(t, err, ErrDraftNotReady)
	assert.Equal(t, "Черновик ещё отправляется — ответьте на него, когда придут кнопки.", UserMessage(err))

	notOurBot := *reply
	notOurBot.Agent = ""

	_, err = env.service.RequestRevisionByReply(ctx, &notOurBot)
	require.ErrorIs(t, err, ErrNotFound, "a reply to a person is not about the draft")

	otherAgent := *reply
	otherAgent.Agent = "marketer"

	_, err = env.service.RequestRevisionByReply(ctx, &otherAgent)
	require.ErrorIs(t, err, ErrNotFound, "another agent has no draft being sent")
}
