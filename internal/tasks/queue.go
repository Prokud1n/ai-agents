package tasks

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/providers/redis"
)

const (
	consumerGroup  = "workers"
	taskIDField    = "task_id"
	delayedSuffix  = ":delayed"
	fetchBlock     = 2 * time.Second // the longest a worker waits for a message before it checks for stop
	claimGrace     = time.Minute     // a message is reclaimed after Timeout + claimGrace without an ack
	moveDueBatch   = 100             // delayed tasks moved to the stream at once
	busyGroupError = "BUSYGROUP"
	noGroupError   = "NOGROUP"
)

// moveDueScript moves due task ids from the delayed set to the stream atomically:
// a crash cannot lose a task between ZREM and XADD.
var moveDueScript = goredis.NewScript(`
local due = redis.call('ZRANGEBYSCORE', KEYS[1], '-inf', ARGV[1], 'LIMIT', 0, tonumber(ARGV[2]))
for _, id in ipairs(due) do
	redis.call('XADD', KEYS[2], '*', ARGV[3], id)
	redis.call('ZREM', KEYS[1], id)
end
return #due
`)

// message is a delivered queue entry.
type message struct {
	ID     string
	TaskID int64
}

// redisQueue delivers task ids to workers through a Redis stream with a consumer group: a message stays
// pending until it is acked, and a message of a dead worker is claimed by another one (XAUTOCLAIM) after
// claimIdle. Retries wait in a sorted set scored by the due time (unix ms) and are moved to the stream
// when due. The queue may lose or duplicate a message: PostgreSQL decides whether a task runs.
type redisQueue struct {
	rdb       *goredis.Client
	stream    string
	delayed   string
	claimIdle time.Duration
	block     time.Duration
	log       *slog.Logger
}

func newRedisQueue(client *redis.Client, cfg *config.Tasks, log *slog.Logger) *redisQueue {
	return &redisQueue{
		rdb:       client.Conn(),
		stream:    cfg.Stream,
		delayed:   cfg.Stream + delayedSuffix,
		claimIdle: cfg.Timeout + claimGrace,
		block:     fetchBlock,
		log:       log,
	}
}

// Init creates the stream and the consumer group if they do not exist. The group starts from the
// beginning of the stream, so messages added before it was created are delivered too.
func (q *redisQueue) Init(ctx context.Context) error {
	err := q.rdb.XGroupCreateMkStream(ctx, q.stream, consumerGroup, "0").Err()
	if err != nil && !strings.HasPrefix(err.Error(), busyGroupError) {
		return fmt.Errorf("failed to create consumer group: %w", err)
	}

	return nil
}

func (q *redisQueue) Enqueue(ctx context.Context, taskID int64) error {
	err := q.rdb.XAdd(ctx, &goredis.XAddArgs{
		Stream: q.stream,
		Values: map[string]any{taskIDField: strconv.FormatInt(taskID, decimalBase)},
	}).Err()
	if err != nil {
		return fmt.Errorf("failed to enqueue task %d: %w", taskID, err)
	}

	return nil
}

// EnqueueAt schedules the task for the moment; a task already scheduled is moved to the new moment.
func (q *redisQueue) EnqueueAt(ctx context.Context, taskID int64, at time.Time) error {
	err := q.rdb.ZAdd(ctx, q.delayed, goredis.Z{
		Score:  float64(at.UnixMilli()),
		Member: strconv.FormatInt(taskID, decimalBase),
	}).Err()
	if err != nil {
		return fmt.Errorf("failed to schedule task %d: %w", taskID, err)
	}

	return nil
}

// MoveDue moves the tasks due by now to the stream and returns their number.
func (q *redisQueue) MoveDue(ctx context.Context, now time.Time) (int, error) {
	moved, err := moveDueScript.Run(ctx, q.rdb, []string{q.delayed, q.stream},
		now.UnixMilli(), moveDueBatch, taskIDField).Int()
	if err != nil {
		return 0, fmt.Errorf("failed to move due tasks: %w", err)
	}

	return moved, nil
}

// Fetch returns a message of a dead consumer if there is one, otherwise waits for a new message up to
// the block time; nil when there is none. Malformed messages are acked and skipped.
func (q *redisQueue) Fetch(ctx context.Context, consumer string) (*message, error) {
	msgs, _, err := q.rdb.XAutoClaim(ctx, &goredis.XAutoClaimArgs{
		Stream:   q.stream,
		Group:    consumerGroup,
		Consumer: consumer,
		MinIdle:  q.claimIdle,
		Start:    "0-0",
		Count:    1,
	}).Result()
	if err != nil {
		return nil, q.fetchError(ctx, "failed to claim stale messages", err)
	}

	if len(msgs) == 0 {
		streams, err := q.rdb.XReadGroup(ctx, &goredis.XReadGroupArgs{
			Group:    consumerGroup,
			Consumer: consumer,
			Streams:  []string{q.stream, ">"},
			Count:    1,
			Block:    q.block,
		}).Result()
		if errors.Is(err, goredis.Nil) {
			return nil, nil
		}

		if err != nil {
			return nil, q.fetchError(ctx, "failed to read messages", err)
		}

		for _, s := range streams {
			msgs = append(msgs, s.Messages...)
		}
	}

	if len(msgs) == 0 {
		return nil, nil
	}

	return q.parse(ctx, &msgs[0])
}

// fetchError recreates the group if the stream was deleted (Redis was flushed): otherwise every read
// would fail with NOGROUP.
func (q *redisQueue) fetchError(ctx context.Context, action string, err error) error {
	if strings.HasPrefix(err.Error(), noGroupError) {
		if initErr := q.Init(ctx); initErr != nil {
			return errors.Join(fmt.Errorf("%s: %w", action, err), initErr)
		}
	}

	return fmt.Errorf("%s: %w", action, err)
}

func (q *redisQueue) parse(ctx context.Context, msg *goredis.XMessage) (*message, error) {
	raw, _ := msg.Values[taskIDField].(string)

	taskID, err := strconv.ParseInt(raw, decimalBase, int64Bits)
	if err == nil && taskID > 0 {
		return &message{ID: msg.ID, TaskID: taskID}, nil
	}

	q.log.ErrorContext(ctx, "tasks: skipping malformed queue message", "message_id", msg.ID, "values", msg.Values)

	if err := q.Ack(ctx, msg.ID); err != nil {
		return nil, err
	}

	return nil, nil
}

// Ack confirms the message and deletes it from the stream, so the stream does not grow.
func (q *redisQueue) Ack(ctx context.Context, messageID string) error {
	_, err := q.rdb.TxPipelined(ctx, func(p goredis.Pipeliner) error {
		p.XAck(ctx, q.stream, consumerGroup, messageID)
		p.XDel(ctx, q.stream, messageID)

		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to ack message %s: %w", messageID, err)
	}

	return nil
}
