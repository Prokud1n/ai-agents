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
	// ConsumerGroup is the consumer group of the workers on the stream.
	ConsumerGroup = "media-workers"
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
	// maxJobLifetime bounds the queue and the run of a job well within recordTTL, so a current job never expires.
	maxJobLifetime = recordTTL / 2
	// presenceTTL: the worker's presence key outlives a few heartbeats.
	presenceTTL = time.Minute

	busyGroupError = "BUSYGROUP"
	noGroupError   = "NOGROUP"
	decimalBase    = 10
	int64Bits      = 64
)

var (
	// unlockScript deletes the lock (or the lease) only if it is still the caller's.
	unlockScript = goredis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then return redis.call('DEL', KEYS[1]) end
return 0
`)
	// replaceScript puts a job in place of the one with a result of the given status: only while that result is
	// there, the job is replaced, its result, start and lock are deleted (a result means the worker is done) and the
	// new job is queued, all at once.
	replaceScript = goredis.NewScript(`
local result = redis.call('GET', KEYS[2])
if not result then return 0 end
local ok, decoded = pcall(cjson.decode, result)
if not ok or type(decoded) ~= 'table' or decoded['status'] ~= ARGV[3] then return 0 end
redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[2])
redis.call('DEL', KEYS[2], KEYS[3], KEYS[5])
redis.call('XADD', KEYS[4], '*', ARGV[4], ARGV[1])
return 1
`)
	// finishScript stores the result (once) only while the job on the board is the given one; 0 when it is not (the
	// app forgot or replaced the job while it ran: the result would answer a job that is not there).
	finishScript = goredis.NewScript(currentJobLua + `
if not current() then return 0 end
redis.call('SET', KEYS[2], ARGV[3], 'NX', 'PX', ARGV[4])
return 1
`)
	// startScript records the first take of the job only while the job on the board is the given one and returns the
	// recorded start; nil when the job is not there (a start left by a forgotten job would shorten the timeout of
	// the job submitted next under the same ID).
	startScript = goredis.NewScript(currentJobLua + `
if not current() then return false end
redis.call('SET', KEYS[2], ARGV[3], 'NX', 'PX', ARGV[4])
return redis.call('GET', KEYS[2])
`)
	// refreshScript extends the lock only if it is still the caller's.
	refreshScript = goredis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then return redis.call('PEXPIRE', KEYS[1], ARGV[2]) end
return 0
`)

	// ErrInvalidConfig: an invalid EDITOR_* setting.
	ErrInvalidConfig = errors.New("invalid editor config")
	// ErrStale: the job on the board is not the given one any more: the app forgot it (a timeout, a failure, no
	// worker) or replaced it with another stage or a new submit under the same ID.
	ErrStale = errors.New("edit job is not on the board")
)

// currentJobLua defines current(): the job stored under KEYS[1] has the stage ARGV[1] (an empty one is AutoStage)
// and the creation time ARGV[2] (createdAt), as Job.Same checks in Go.
var currentJobLua = fmt.Sprintf(`
local function current()
  local stored = redis.call('GET', KEYS[1])
  if not stored then return false end
  local ok, job = pcall(cjson.decode, stored)
  if not ok or type(job) ~= 'table' then return false end
  local stage = job['stage']
  if type(stage) ~= 'string' or stage == '' then stage = '%s' end
  return stage == ARGV[1] and job['created_at'] == ARGV[2]
end
`, AutoStage)

// Board is where the app and the media worker exchange jobs and results, in Redis:
//
//	<stream>                 stream of submitted jobs, read by the consumer group of the workers
//	<stream>:job:<id>        the submitted job (the app submits a job only when it has none)
//	<stream>:result:<id>     the result
//	<stream>:started:<id>    when a worker took the job (the job timeout counts from it)
//	<stream>:lock:<id>       the worker editing the job, refreshed by its heartbeat
//	<stream>:lease:<id>      the token of the local edit (make edit) waiting for the job (Job.Lease), refreshed by it
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
	// A job lives on the board for recordTTL: one that waits and runs longer would expire while it is current, and
	// the worker skips a job that is not on the board.
	case cfg.QueueTimeout+cfg.JobTimeout >= maxJobLifetime:
		return nil, fmt.Errorf("EDITOR_QUEUE_TIMEOUT + EDITOR_JOB_TIMEOUT must be under %s: %w", maxJobLifetime,
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
func (b *Board) leaseKey(id string) string   { return b.stream + ":lease:" + id }
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
	return b.replace(ctx, job, PlannedStatus)
}

// Retry queues the job again in place of its failed result (a temporary failure of a decided render: the same
// edit, new deadlines); false if the job has no failed result.
func (b *Board) Retry(ctx context.Context, job *Job) (bool, error) {
	return b.replace(ctx, job, FailedStatus)
}

func (b *Board) replace(ctx context.Context, job *Job, status Status) (bool, error) {
	data, err := json.Marshal(job)
	if err != nil {
		return false, fmt.Errorf("failed to encode job: %w", err)
	}

	keys := []string{b.jobKey(job.ID), b.resultKey(job.ID), b.startedKey(job.ID), b.stream, b.lockKey(job.ID)}

	n, err := replaceScript.Run(ctx, b.rdb, keys, data, recordTTL.Milliseconds(), string(status), jobField).Int()
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
// a crash keeps its start, so its timeout is not extended). ErrStale when the job on the board is not this one: the
// start is not recorded.
func (b *Board) MarkStarted(ctx context.Context, job *Job, now time.Time) (time.Time, error) {
	keys := []string{b.jobKey(job.ID), b.startedKey(job.ID)}

	raw, err := startScript.Run(ctx, b.rdb, keys, string(job.EffectiveStage()), createdAt(job),
		now.UTC().Format(time.RFC3339Nano), recordTTL.Milliseconds()).Text()
	if errors.Is(err, goredis.Nil) {
		return time.Time{}, fmt.Errorf("failed to mark job %s started: %w", job.ID, ErrStale)
	}

	if err != nil {
		return time.Time{}, fmt.Errorf("failed to mark job %s started: %w", job.ID, err)
	}

	started, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to parse the start of job %s: %w", job.ID, err)
	}

	return started, nil
}

// createdAt is Job.CreatedAt as it is stored in the job's JSON (time.Time marshals as RFC 3339 with nanoseconds).
func createdAt(job *Job) string {
	return job.CreatedAt.Format(time.RFC3339Nano)
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

// Lease holds the lease of the job for the token for ttl (a new token takes the lease over): the waiting side calls it
// again before ttl runs out. A job with Job.Lease is current for the worker only while its token holds the lease.
func (b *Board) Lease(ctx context.Context, jobID, token string, ttl time.Duration) error {
	if token == "" || ttl <= 0 {
		return fmt.Errorf("lease of job %s needs a token and a positive ttl: %w", jobID, ErrInvalidJob)
	}

	if err := b.rdb.Set(ctx, b.leaseKey(jobID), token, ttl).Err(); err != nil {
		return fmt.Errorf("failed to lease job %s: %w", jobID, err)
	}

	return nil
}

// ReleaseLease deletes the lease of the job if the token still holds it.
func (b *Board) ReleaseLease(ctx context.Context, jobID, token string) error {
	if err := unlockScript.Run(ctx, b.rdb, []string{b.leaseKey(jobID)}, token).Err(); err != nil {
		return fmt.Errorf("failed to release the lease of job %s: %w", jobID, err)
	}

	return nil
}

// LeaseHeld reports whether the job's lease is held by its token (Job.Lease); a job without a lease is always held.
func (b *Board) LeaseHeld(ctx context.Context, job *Job) (bool, error) {
	if job.Lease == "" {
		return true, nil
	}

	token, err := b.rdb.Get(ctx, b.leaseKey(job.ID)).Result()
	if errors.Is(err, goredis.Nil) {
		return false, nil
	}

	if err != nil {
		return false, fmt.Errorf("failed to read the lease of job %s: %w", job.ID, err)
	}

	return token == job.Lease, nil
}
