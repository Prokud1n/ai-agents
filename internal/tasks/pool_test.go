package tasks

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// panickingRunner panics on every run, like an executor with a bug.
type panickingRunner struct{}

func (panickingRunner) Run(context.Context, int64) error {
	panic("executor bug")
}

func (panickingRunner) Sweep(context.Context, bool) error {
	panic("sweeper bug")
}

// recordingQueue records acks and schedules.
type recordingQueue struct {
	mu        sync.Mutex
	acked     []string
	scheduled []int64
}

func (*recordingQueue) Init(context.Context) error {
	return nil
}

func (*recordingQueue) Fetch(context.Context, string) (*message, error) {
	return nil, errors.New("not used")
}

func (q *recordingQueue) Ack(_ context.Context, messageID string) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	q.acked = append(q.acked, messageID)

	return nil
}

func (q *recordingQueue) EnqueueAt(_ context.Context, taskID int64, _ time.Time) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	q.scheduled = append(q.scheduled, taskID)

	return nil
}

func (*recordingQueue) MoveDue(context.Context, time.Time) (int, error) {
	return 0, nil
}

func TestPoolHandleRecoversPanic(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	queue := &recordingQueue{}
	pool := NewPool(&cfg, panickingRunner{}, queue, slog.New(slog.DiscardHandler))

	require.NotPanics(t, func() { pool.handle(context.Background(), &message{ID: "1-0", TaskID: 42}) })

	assert.Equal(t, []string{"1-0"}, queue.acked, "the message is acked")
	assert.Equal(t, []int64{42}, queue.scheduled, "the task is scheduled again")

	err := pool.run(context.Background(), 42)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "panic")

	require.NotPanics(t, func() { err = pool.sweepOnce(context.Background(), true) })
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sweeper bug")
}
