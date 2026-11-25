package editlocal

import (
	"context"
	"errors"
	"fmt"
	"math"

	goredis "github.com/redis/go-redis/v9"

	"github.com/Prokud1n/ai-agents/internal/providers/redis"
)

const (
	// TaskIDBase: the synthetic task IDs of local edits are above it. The tasks of the app get their IDs from a
	// PostgreSQL bigserial, which never gets near 10^18, so a local job, its result files and the done messages never
	// meet those of a real task (the app's waker finds no such task and leaves it alone).
	TaskIDBase int64 = 1_000_000_000_000_000_000
	// SequenceKey is the Redis counter of local edits: TaskIDBase + its value is the task ID.
	SequenceKey = "edit-local:seq"
)

// ErrInvalidSequence: the counter of local edits gave a value that makes no task ID.
var ErrInvalidSequence = errors.New("invalid local edit sequence")

// TaskID is the synthetic task ID of the seq-th local edit (seq starts at 1).
func TaskID(seq int64) (int64, error) {
	if seq <= 0 || seq > math.MaxInt64-TaskIDBase {
		return 0, fmt.Errorf("sequence value %d: %w", seq, ErrInvalidSequence)
	}

	return TaskIDBase + seq, nil
}

// IsLocalTaskID reports whether the task ID is one of a local edit.
func IsLocalTaskID(id int64) bool {
	return id > TaskIDBase
}

// RedisSequence gives the task IDs of local edits from a Redis counter: every run gets a new one, so a new edit never
// finds the job or the files of an earlier one.
type RedisSequence struct {
	rdb *goredis.Client
	key string
}

func NewRedisSequence(client *redis.Client, key string) *RedisSequence {
	return &RedisSequence{rdb: client.Conn(), key: key}
}

// NextTaskID increments the counter and returns the task ID of its new value.
func (s *RedisSequence) NextTaskID(ctx context.Context) (int64, error) {
	seq, err := s.rdb.Incr(ctx, s.key).Result()
	if err != nil {
		return 0, fmt.Errorf("failed to increment %s: %w", s.key, err)
	}

	return TaskID(seq)
}
