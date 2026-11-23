package editjob

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// Delivery is a stream entry given to a worker. Job is nil for a malformed entry (it is acked and skipped).
type Delivery struct {
	MessageID string
	Job       *Job
}

// Init creates the consumer group of the workers if it does not exist; it starts from the beginning of the stream,
// so jobs submitted before the first worker started are taken too.
func (b *Board) Init(ctx context.Context) error {
	err := b.rdb.XGroupCreateMkStream(ctx, b.stream, consumerGroup, "0").Err()
	if err != nil && !strings.HasPrefix(err.Error(), busyGroupError) {
		return fmt.Errorf("failed to create consumer group: %w", err)
	}

	return nil
}

// Fetch returns an entry of a dead worker if there is one, otherwise waits for a new entry up to the block time;
// nil when there is none.
func (b *Board) Fetch(ctx context.Context, consumer string) (*Delivery, error) {
	msgs, _, err := b.rdb.XAutoClaim(ctx, &goredis.XAutoClaimArgs{
		Stream: b.stream, Group: consumerGroup, Consumer: consumer, MinIdle: b.claimIdle, Start: "0-0", Count: 1,
	}).Result()
	if err != nil {
		return nil, b.fetchError(ctx, "failed to claim stale jobs", err)
	}

	if len(msgs) == 0 {
		streams, err := b.rdb.XReadGroup(ctx, &goredis.XReadGroupArgs{
			Group: consumerGroup, Consumer: consumer, Streams: []string{b.stream, ">"}, Count: 1, Block: b.block,
		}).Result()
		if errors.Is(err, goredis.Nil) {
			return nil, nil
		}

		if err != nil {
			return nil, b.fetchError(ctx, "failed to read jobs", err)
		}

		for _, s := range streams {
			msgs = append(msgs, s.Messages...)
		}
	}

	if len(msgs) == 0 {
		return nil, nil
	}

	return parseDelivery(&msgs[0]), nil
}

// fetchError recreates the group if the stream was deleted (Redis was flushed).
func (b *Board) fetchError(ctx context.Context, action string, err error) error {
	if strings.HasPrefix(err.Error(), noGroupError) {
		if initErr := b.Init(ctx); initErr != nil {
			return errors.Join(fmt.Errorf("%s: %w", action, err), initErr)
		}
	}

	return fmt.Errorf("%s: %w", action, err)
}

func parseDelivery(msg *goredis.XMessage) *Delivery {
	d := &Delivery{MessageID: msg.ID}

	raw, _ := msg.Values[jobField].(string)

	var job Job
	if err := json.Unmarshal([]byte(raw), &job); err == nil && job.Validate() == nil {
		d.Job = &job
	}

	return d
}

// Ack confirms the entry and deletes it from the stream, so the stream does not grow.
func (b *Board) Ack(ctx context.Context, messageID string) error {
	_, err := b.rdb.TxPipelined(ctx, func(p goredis.Pipeliner) error {
		p.XAck(ctx, b.stream, consumerGroup, messageID)
		p.XDel(ctx, b.stream, messageID)

		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to ack job entry %s: %w", messageID, err)
	}

	return nil
}

// Lock takes the job for the owner for the lock lifetime (LockTTL); false if another worker holds it.
func (b *Board) Lock(ctx context.Context, jobID, owner string) (bool, error) {
	locked, err := b.rdb.SetNX(ctx, b.lockKey(jobID), owner, b.lockTTL).Result()
	if err != nil {
		return false, fmt.Errorf("failed to lock job %s: %w", jobID, err)
	}

	return locked, nil
}

// Refresh extends the owner's lock; false if the lock is lost (it expired and another worker took the job).
func (b *Board) Refresh(ctx context.Context, jobID, owner string) (bool, error) {
	n, err := refreshScript.Run(ctx, b.rdb, []string{b.lockKey(jobID)}, owner, b.lockTTL.Milliseconds()).Int()
	if err != nil {
		return false, fmt.Errorf("failed to refresh lock of job %s: %w", jobID, err)
	}

	return n == 1, nil
}

// Unlock releases the owner's lock; a lock of another worker is left alone.
func (b *Board) Unlock(ctx context.Context, jobID, owner string) error {
	if err := unlockScript.Run(ctx, b.rdb, []string{b.lockKey(jobID)}, owner).Err(); err != nil {
		return fmt.Errorf("failed to unlock job %s: %w", jobID, err)
	}

	return nil
}

// Finish stores the result and tells the app the task can be run. The result is stored once: a result already
// there (a duplicate run) is kept. The message is best effort: the app also polls.
func (b *Board) Finish(ctx context.Context, job *Job, result *Result) error {
	data, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("failed to encode result: %w", err)
	}

	if err := b.rdb.SetNX(ctx, b.resultKey(job.ID), data, recordTTL).Err(); err != nil {
		return fmt.Errorf("failed to store result of job %s: %w", job.ID, err)
	}

	b.NotifyDone(ctx, job)

	return nil
}

// NotifyDone tells the app that the job has a result; a failure is only logged.
func (b *Board) NotifyDone(ctx context.Context, job *Job) {
	if err := b.rdb.Publish(ctx, b.doneChannel(), strconv.FormatInt(job.TaskID, decimalBase)).Err(); err != nil {
		b.log.WarnContext(ctx, "editjob: failed to announce finished job, the app polls it", "error", err, "job_id", job.ID)
	}
}

// Heartbeat marks the worker present for presenceTTL (<stream>:worker shows a live worker, e.g. to an operator).
func (b *Board) Heartbeat(ctx context.Context, owner string) error {
	if err := b.rdb.Set(ctx, b.presenceKey(), owner, presenceTTL).Err(); err != nil {
		return fmt.Errorf("failed to mark media worker present: %w", err)
	}

	return nil
}

// HasResult reports whether the job has a result.
func (b *Board) HasResult(ctx context.Context, jobID string) (bool, error) {
	n, err := b.rdb.Exists(ctx, b.resultKey(jobID)).Result()
	if err != nil {
		return false, fmt.Errorf("failed to check result of job %s: %w", jobID, err)
	}

	return n > 0, nil
}

// LockTTL is how long a lock lives without a refresh: the worker refreshes it well within.
func (b *Board) LockTTL() time.Duration {
	return b.lockTTL
}
