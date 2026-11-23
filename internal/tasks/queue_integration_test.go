package tasks

import (
	"context"
	"log/slog"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/pgtest"
	"github.com/Prokud1n/ai-agents/internal/redistest"
)

const testBlock = 100 * time.Millisecond

func newTestQueue(t *testing.T, claimIdle time.Duration) *redisQueue {
	t.Helper()

	client := redistest.Connect(t)
	cfg := testConfig()
	cfg.Stream = redistest.Prefix(t, client) + "tasks"

	q := newRedisQueue(client, &cfg, slog.New(slog.DiscardHandler))
	q.claimIdle = claimIdle
	q.block = testBlock

	require.NoError(t, q.Init(context.Background()))
	require.NoError(t, q.Init(context.Background()), "init is idempotent")

	return q
}

func TestIntegrationRedisQueue(t *testing.T) {
	t.Parallel()

	q := newTestQueue(t, time.Hour)
	ctx := context.Background()

	msg, err := q.Fetch(ctx, "a")
	require.NoError(t, err)
	assert.Nil(t, msg, "empty queue")

	require.NoError(t, q.Enqueue(ctx, 42))

	msg, err = q.Fetch(ctx, "a")
	require.NoError(t, err)
	require.NotNil(t, msg)
	assert.Equal(t, int64(42), msg.TaskID)

	require.NoError(t, q.Ack(ctx, msg.ID))

	msg, err = q.Fetch(ctx, "b")
	require.NoError(t, err)
	assert.Nil(t, msg, "an acked message is not delivered again")

	length, err := q.rdb.XLen(ctx, q.stream).Result()
	require.NoError(t, err)
	assert.Zero(t, length, "acked messages are deleted from the stream")

	// A malformed message is skipped.
	require.NoError(t, q.rdb.XAdd(ctx, &goredis.XAddArgs{Stream: q.stream, Values: map[string]any{"x": "1"}}).Err())

	msg, err = q.Fetch(ctx, "a")
	require.NoError(t, err)
	assert.Nil(t, msg)
}

func TestIntegrationRedisQueueDelayed(t *testing.T) {
	t.Parallel()

	q := newTestQueue(t, time.Hour)
	ctx := context.Background()
	due := time.Now().Add(time.Hour)

	require.NoError(t, q.EnqueueAt(ctx, 7, due))
	require.NoError(t, q.EnqueueAt(ctx, 7, due), "scheduling again keeps one entry")

	moved, err := q.MoveDue(ctx, due.Add(-time.Second))
	require.NoError(t, err)
	assert.Zero(t, moved, "not due yet")

	moved, err = q.MoveDue(ctx, due)
	require.NoError(t, err)
	assert.Equal(t, 1, moved)

	moved, err = q.MoveDue(ctx, due)
	require.NoError(t, err)
	assert.Zero(t, moved, "moved once")

	msg, err := q.Fetch(ctx, "a")
	require.NoError(t, err)
	require.NotNil(t, msg)
	assert.Equal(t, int64(7), msg.TaskID)
}

func TestIntegrationRedisQueueReclaimsMessagesOfDeadWorkers(t *testing.T) {
	t.Parallel()

	const claimIdle = 200 * time.Millisecond

	q := newTestQueue(t, claimIdle)
	ctx := context.Background()

	require.NoError(t, q.Enqueue(ctx, 9))

	// Worker "a" takes the message and dies without an ack.
	msg, err := q.Fetch(ctx, "a")
	require.NoError(t, err)
	require.NotNil(t, msg)

	other, err := q.Fetch(ctx, "b")
	require.NoError(t, err)
	assert.Nil(t, other, "a pending message is not given to another worker before claimIdle")

	time.Sleep(claimIdle)

	claimed, err := q.Fetch(ctx, "b")
	require.NoError(t, err)
	require.NotNil(t, claimed)
	assert.Equal(t, msg.ID, claimed.ID)
	assert.Equal(t, int64(9), claimed.TaskID)

	// The stream is gone (Redis was flushed): the group is recreated.
	require.NoError(t, q.rdb.Del(ctx, q.stream).Err())

	_, err = q.Fetch(ctx, "a")
	require.Error(t, err)

	require.NoError(t, q.Enqueue(ctx, 10))

	msg, err = q.Fetch(ctx, "a")
	require.NoError(t, err)
	require.NotNil(t, msg)
	assert.Equal(t, int64(10), msg.TaskID)
}

// TestIntegrationPoolRunsTasks runs the whole path: Create → Redis → worker → draft, and a revision.
func TestIntegrationPoolRunsTasks(t *testing.T) {
	t.Parallel()

	env := newTestEnv(t, pgtest.Connect(t), fakeResult{text: "v1"}, fakeResult{text: "v2"})
	q := newTestQueue(t, time.Hour)
	env.service.queue = q
	env.service.now = time.Now

	cfg := testConfig()
	cfg.Stream = q.stream

	pool := NewPool(&cfg, env.service, q, slog.New(slog.DiscardHandler))

	ctx := context.Background()
	require.NoError(t, pool.Start(ctx))

	// Stop is safe to call twice: this one only matters when the test fails before its own Stop.
	t.Cleanup(func() { require.NoError(t, pool.Stop(context.Background())) })

	task, _, err := env.service.Create(ctx, testTaskData(90))
	require.NoError(t, err)

	waitAwaiting(t, env.service, task.ID, 1)

	_, err = env.service.RequestRevision(ctx, &RequestRevisionData{TaskID: task.ID, Version: 1, Text: "короче"})
	require.NoError(t, err)

	waitAwaiting(t, env.service, task.ID, 2)

	// The status is committed before the worker delivers the draft: wait for the delivery too.
	require.Eventually(t, func() bool {
		return len(env.notifier.Drafts()) == 2
	}, 15*time.Second, 50*time.Millisecond, "the drafts of both versions are not delivered")

	stopCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	require.NoError(t, pool.Stop(stopCtx))
}

// waitAwaiting waits until the workers make the version and it awaits approval.
func waitAwaiting(t *testing.T, s *Service, taskID int64, version int) {
	t.Helper()

	require.Eventually(t, func() bool {
		task, err := s.Get(context.Background(), taskID)

		return err == nil && task.Status == AwaitingApprovalStatus && task.Version == version
	}, 15*time.Second, 50*time.Millisecond, "task %d did not reach v%d awaiting approval", taskID, version)
}
