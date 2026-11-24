package editjob

import (
	"context"
	"encoding/json"
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
	consumerGroup = "media-workers"
	jobField      = "job"
	fetchBlock    = 2 * time.Second // the longest a worker waits for a job before it checks for stop
	// defaultLockTTL is how long a job lock lives without a heartbeat: a worker that died mid-job frees its job
	// by then.
	defaultLockTTL = time.Minute
	// claimIdle: a stream entry not acked for this long belongs to a dead worker (a live one refreshes the job's
	// lock, and a claimed entry with a live lock is skipped).
	claimIdle = 2 * defaultLockTTL
	// recordTTL keeps job states and results long enough for retries and the next step (T3.4c), then Redis drops
	// them; the files stay in S3.
	recordTTL = 7 * 24 * time.Hour
	// presenceTTL: the worker's presence key outlives a few heartbeats.
	presenceTTL = time.Minute

	busyGroupError = "BUSYGROUP"
	noGroupError   = "NOGROUP"
	decimalBase    = 10
	int64Bits      = 64
)

var (
	// unlockScript deletes the lock only if it is still the caller's.
	unlockScript = goredis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then return redis.call('DEL', KEYS[1]) end
return 0
`)
	// replaceScript puts the next stage of a planned job in its place: only while the job's result is planned, the
	// job is replaced, its result and start are deleted and the new job is queued, all at once.
	replaceScript = goredis.NewScript(`
local result = redis.call('GET', KEYS[2])
if not result then return 0 end
local ok, decoded = pcall(cjson.decode, result)
if not ok or type(decoded) ~= 'table' or decoded['status'] ~= ARGV[3] then return 0 end
redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[2])
redis.call('DEL', KEYS[2], KEYS[3])
redis.call('XADD', KEYS[4], '*', ARGV[4], ARGV[1])
return 1
`)
	// refreshScript extends the lock only if it is still the caller's.
	refreshScript = goredis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then return redis.call('PEXPIRE', KEYS[1], ARGV[2]) end
return 0
`)

	// ErrInvalidConfig: an invalid EDITOR_* setting.
	ErrInvalidConfig = errors.New("invalid editor config")
)

// Board is where the app and the media worker exchange jobs and results, in Redis:
//
//	<stream>                 stream of submitted jobs, read by the consumer group of the workers
//	<stream>:job:<id>        the submitted job (the app submits a job only when it has none)
//	<stream>:result:<id>     the result
//	<stream>:started:<id>    when a worker took the job (the job timeout counts from it)
//	<stream>:lock:<id>       the worker editing the job, refreshed by its heartbeat
//	<stream>:done            channel: the task ID of a finished job, so the app runs the task at once
//	<stream>:worker          presence of a live worker
type Board struct {
	rdb       *goredis.Client
	stream    string
	block     time.Duration
	claimIdle time.Duration
	lockTTL   time.Duration
	log       *slog.Logger
}

// NewBoard fails on an invalid EDITOR_* config, so a typo stops the start.
func NewBoard(client *redis.Client, cfg *config.Editor, log *slog.Logger) (*Board, error) {
	switch {
	case strings.TrimSpace(cfg.Stream) == "":
		return nil, fmt.Errorf("EDITOR_STREAM is empty: %w", ErrInvalidConfig)
	case cfg.JobTimeout <= 0 || cfg.PollInterval <= 0 || cfg.QueueTimeout <= 0 || cfg.WorkerWait <= 0:
		return nil, fmt.Errorf("EDITOR_JOB_TIMEOUT, _POLL_INTERVAL, _QUEUE_TIMEOUT and _WORKER_WAIT must be positive: %w",
			ErrInvalidConfig)
	}

	return &Board{
		rdb: client.Conn(), stream: cfg.Stream, block: fetchBlock, claimIdle: claimIdle, lockTTL: defaultLockTTL, log: log,
	}, nil
}

func (b *Board) jobKey(id string) string     { return b.stream + ":job:" + id }
func (b *Board) resultKey(id string) string  { return b.stream + ":result:" + id }
func (b *Board) lockKey(id string) string    { return b.stream + ":lock:" + id }
func (b *Board) startedKey(id string) string { return b.stream + ":started:" + id }
func (b *Board) doneChannel() string         { return b.stream + ":done" }
func (b *Board) presenceKey() string         { return b.stream + ":worker" }

// Submit stores the job and puts it into the stream; false if the job is already submitted (nothing is added).
func (b *Board) Submit(ctx context.Context, job *Job) (bool, error) {
	data, err := json.Marshal(job)
	if err != nil {
		return false, fmt.Errorf("failed to encode job: %w", err)
	}

	created, err := b.rdb.SetNX(ctx, b.jobKey(job.ID), data, recordTTL).Result()
	if err != nil {
		return false, fmt.Errorf("failed to store job %s: %w", job.ID, err)
	}

	if !created {
		return false, nil
	}

	if err := b.rdb.XAdd(ctx, &goredis.XAddArgs{Stream: b.stream, Values: map[string]any{jobField: data}}).Err(); err != nil {
		// Without its stream entry nobody would take the job: forget it, the next run submits it again.
		if delErr := b.rdb.Del(context.WithoutCancel(ctx), b.jobKey(job.ID)).Err(); delErr != nil {
			b.log.ErrorContext(ctx, "editjob: failed to forget a job that was not queued", "error", delErr, "job_id", job.ID)
		}

		return false, fmt.Errorf("failed to queue job %s: %w", job.ID, err)
	}

	return true, nil
}

// Replace puts the job (the next stage of a job with a planned result, the same ID) in place of the planned one and
// queues it; false if the job's result is not planned (the job was replaced already, or forgotten): nothing changes.
// A delivery of the replaced job left in the stream is skipped by the worker (its stage differs from the stored job).
func (b *Board) Replace(ctx context.Context, job *Job) (bool, error) {
	data, err := json.Marshal(job)
	if err != nil {
		return false, fmt.Errorf("failed to encode job: %w", err)
	}

	keys := []string{b.jobKey(job.ID), b.resultKey(job.ID), b.startedKey(job.ID), b.stream}

	n, err := replaceScript.Run(ctx, b.rdb, keys, data, recordTTL.Milliseconds(), string(PlannedStatus), jobField).Int()
	if err != nil {
		return false, fmt.Errorf("failed to replace job %s: %w", job.ID, err)
	}

	return n == 1, nil
}

// Job returns the submitted job; false if there is none.
func (b *Board) Job(ctx context.Context, id string) (Job, bool, error) {
	var job Job

	found, err := b.get(ctx, b.jobKey(id), &job)
	if err != nil {
		return Job{}, false, fmt.Errorf("failed to get job %s: %w", id, err)
	}

	return job, found, nil
}

// Result returns the result of the job; false if there is none yet.
func (b *Board) Result(ctx context.Context, id string) (Result, bool, error) {
	var result Result

	found, err := b.get(ctx, b.resultKey(id), &result)
	if err != nil {
		return Result{}, false, fmt.Errorf("failed to get result of job %s: %w", id, err)
	}

	return result, found, nil
}

// Forget deletes the job and its result, so the next run submits the job again.
func (b *Board) Forget(ctx context.Context, id string) error {
	if err := b.rdb.Del(ctx, b.jobKey(id), b.resultKey(id), b.startedKey(id)).Err(); err != nil {
		return fmt.Errorf("failed to forget job %s: %w", id, err)
	}

	return nil
}

func (b *Board) get(ctx context.Context, key string, v any) (bool, error) {
	data, err := b.rdb.Get(ctx, key).Bytes()
	if errors.Is(err, goredis.Nil) {
		return false, nil
	}

	if err != nil {
		return false, fmt.Errorf("failed to read %s: %w", key, err)
	}

	if err := json.Unmarshal(data, v); err != nil {
		return false, fmt.Errorf("failed to decode %s: %w", key, err)
	}

	return true, nil
}

// ListenDone calls handle with the task ID of every job finished while it listens, until ctx is done. Messages
// sent while nobody listens are lost: the app also checks its jobs every EDITOR_POLL_INTERVAL.
func (b *Board) ListenDone(ctx context.Context, handle func(ctx context.Context, taskID int64)) error {
	ps := b.rdb.Subscribe(ctx, b.doneChannel())
	defer func() { _ = ps.Close() }() // best effort: the connection goes back to the pool

	if _, err := ps.Receive(ctx); err != nil {
		return fmt.Errorf("failed to subscribe to finished jobs: %w", err)
	}

	messages := ps.Channel()

	for {
		select {
		case <-ctx.Done():
			return nil
		case msg, ok := <-messages:
			if !ok {
				return nil
			}

			taskID, err := strconv.ParseInt(msg.Payload, decimalBase, int64Bits)
			if err != nil || taskID <= 0 {
				b.log.WarnContext(ctx, "editjob: skipping malformed finished job message", "payload_bytes", len(msg.Payload))

				continue
			}

			handle(ctx, taskID)
		}
	}
}

// MarkStarted records that a worker took the job, once: it returns the moment of the first take (a job restarted after
// a crash keeps its start, so its timeout is not extended).
func (b *Board) MarkStarted(ctx context.Context, id string, now time.Time) (time.Time, error) {
	key := b.startedKey(id)

	if err := b.rdb.SetNX(ctx, key, now.UTC().Format(time.RFC3339Nano), recordTTL).Err(); err != nil {
		return time.Time{}, fmt.Errorf("failed to mark job %s started: %w", id, err)
	}

	started, found, err := b.Started(ctx, id)
	if err != nil {
		return time.Time{}, err
	}

	if !found { // expired between the two calls: a week later, only in theory
		return now, nil
	}

	return started, nil
}

// Started returns when a worker took the job; false if none has.
func (b *Board) Started(ctx context.Context, id string) (time.Time, bool, error) {
	raw, err := b.rdb.Get(ctx, b.startedKey(id)).Result()
	if errors.Is(err, goredis.Nil) {
		return time.Time{}, false, nil
	}

	if err != nil {
		return time.Time{}, false, fmt.Errorf("failed to read the start of job %s: %w", id, err)
	}

	started, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("failed to parse the start of job %s: %w", id, err)
	}

	return started, true, nil
}

// Locked reports whether a worker holds the job's lock now.
func (b *Board) Locked(ctx context.Context, id string) (bool, error) {
	n, err := b.rdb.Exists(ctx, b.lockKey(id)).Result()
	if err != nil {
		return false, fmt.Errorf("failed to check the lock of job %s: %w", id, err)
	}

	return n > 0, nil
}

// WorkerAlive reports whether a worker has sent a heartbeat within presenceTTL.
func (b *Board) WorkerAlive(ctx context.Context) (bool, error) {
	n, err := b.rdb.Exists(ctx, b.presenceKey()).Result()
	if err != nil {
		return false, fmt.Errorf("failed to check media worker presence: %w", err)
	}

	return n > 0, nil
}
