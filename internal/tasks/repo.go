package tasks

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Prokud1n/ai-agents/internal/providers/postgres"
)

const sweepBatch = 100 // tasks handled by one sweep of each kind

type repo struct {
	db *postgres.Client
}

func newRepo(db *postgres.Client) *repo {
	return &repo{db: db}
}

const taskColumns = `id, agent, kind, input, media, source_chat_id, source_topic_id, source_message_id, author_id,
	status, version, attempts, last_error, run_id, run_after, enqueued_at, started_at, created_at, updated_at`

const selectTask = `SELECT ` + taskColumns + ` FROM tasks`

func scanTask(row pgx.Row) (Task, error) {
	var t Task

	err := row.Scan(&t.ID, &t.Agent, &t.Kind, &t.Input, &t.Media, &t.Source.ChatID, &t.Source.TopicID,
		&t.Source.MessageID, &t.AuthorID, &t.Status, &t.Version, &t.Attempts, &t.LastError, &t.RunID, &t.RunAfter,
		&t.EnqueuedAt, &t.StartedAt, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Task{}, ErrNotFound
	}

	if err != nil {
		return Task{}, fmt.Errorf("failed to scan task: %w", err)
	}

	return t, nil
}

// Create inserts a queued task; if a task from the same source message (chat, message id) exists, it is
// returned with created = false. ON CONFLICT waits for a concurrent insert of the same source to commit, so the
// following select sees its row.
func (r *repo) Create(ctx context.Context, data *CreateTaskData, now time.Time) (task Task, created bool, err error) {
	const insert = `
		INSERT INTO tasks (agent, kind, input, media, source_chat_id, source_topic_id, source_message_id,
		                   author_id, status, run_after, enqueued_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $10, $10, $10)
		ON CONFLICT (source_chat_id, source_message_id) DO NOTHING
		RETURNING ` + taskColumns

	media := data.Media
	if media == nil {
		media = []string{}
	}

	conn := r.db.GetWriteConnection()

	task, err = scanTask(conn.QueryRow(ctx, insert, data.Agent, data.Kind, data.Input, media,
		data.Source.ChatID, data.Source.TopicID, data.Source.MessageID, data.AuthorID, string(QueuedStatus), now))
	if err == nil {
		return task, true, nil
	}

	if !errors.Is(err, ErrNotFound) {
		return Task{}, false, fmt.Errorf("failed to insert task: %w", err)
	}

	task, err = scanTask(conn.QueryRow(ctx,
		selectTask+` WHERE source_chat_id = $1 AND source_message_id = $2`,
		data.Source.ChatID, data.Source.MessageID))
	if err != nil {
		return Task{}, false, fmt.Errorf("failed to get task by source: %w", err)
	}

	return task, false, nil
}

func (r *repo) Get(ctx context.Context, id int64) (Task, error) {
	task, err := scanTask(r.db.GetReadConnection().QueryRow(ctx, selectTask+` WHERE id = $1`, id))
	if err != nil {
		return Task{}, fmt.Errorf("failed to get task %d: %w", id, err)
	}

	return task, nil
}

// Update locks the task, applies the updater and writes the change with its rows in one transaction,
// then returns the task as stored.
func (r *repo) Update(ctx context.Context, id int64, fn UpdaterFn) (Task, error) {
	var updated Task

	err := pgx.BeginFunc(ctx, r.db.GetWriteConnection(), func(tx pgx.Tx) error {
		current, err := scanTask(tx.QueryRow(ctx, selectTask+` WHERE id = $1 FOR UPDATE`, id))
		if err != nil {
			return fmt.Errorf("failed to lock task %d: %w", id, err)
		}

		change, err := fn(current)
		if err != nil {
			return err
		}

		if err := writeChange(ctx, tx, &change); err != nil {
			return err
		}

		updated, err = scanTask(tx.QueryRow(ctx, selectTask+` WHERE id = $1`, id))

		return err
	})
	if err != nil {
		return Task{}, fmt.Errorf("failed to update task: %w", err)
	}

	return updated, nil
}

func writeChange(ctx context.Context, tx pgx.Tx, change *Change) error {
	const update = `
		UPDATE tasks
		SET status = $2, version = $3, attempts = $4, last_error = $5, run_id = $6, run_after = $7,
		    enqueued_at = $8, started_at = $9, updated_at = $10
		WHERE id = $1`

	t := &change.Task

	if _, err := tx.Exec(ctx, update, t.ID, string(t.Status), t.Version, t.Attempts, t.LastError, t.RunID,
		t.RunAfter, t.EnqueuedAt, t.StartedAt, t.UpdatedAt); err != nil {
		return fmt.Errorf("failed to write task: %w", err)
	}

	if v := change.Version; v != nil {
		if _, err := tx.Exec(ctx, `
			INSERT INTO task_versions (task_id, version, content, delivery_after, created_at)
			VALUES ($1, $2, $3, $4, $4)`,
			v.TaskID, v.Version, v.Content, v.CreatedAt); err != nil {
			return fmt.Errorf("failed to insert task version: %w", err)
		}
	}

	if f := change.Feedback; f != nil {
		if _, err := tx.Exec(ctx, `
			INSERT INTO task_feedback (task_id, version, kind, text, author_id, created_at)
			VALUES ($1, $2, $3, $4, $5, $6)`,
			f.TaskID, f.Version, string(f.Kind), f.Text, f.AuthorID, f.CreatedAt); err != nil {
			return fmt.Errorf("failed to insert task feedback: %w", err)
		}
	}

	if d := change.Redeliver; d != nil {
		if _, err := tx.Exec(ctx, `
			UPDATE task_versions
			SET draft_message_id = NULL, delivery_attempts = 0, delivery_after = $3, delivery_note = $4,
			    old_draft_message_ids = CASE WHEN draft_message_id > 0
			        THEN array_append(old_draft_message_ids, draft_message_id) ELSE old_draft_message_ids END
			WHERE task_id = $1 AND version = $2`,
			t.ID, d.Version, t.UpdatedAt, d.Note); err != nil {
			return fmt.Errorf("failed to reset draft delivery: %w", err)
		}
	}

	return nil
}

func (r *repo) GetVersion(ctx context.Context, taskID int64, version int) (Version, error) {
	var v Version

	err := r.db.GetReadConnection().QueryRow(ctx, `
		SELECT task_id, version, content, draft_message_id, created_at
		FROM task_versions
		WHERE task_id = $1 AND version = $2`, taskID, version,
	).Scan(&v.TaskID, &v.Version, &v.Content, &v.DraftMessageID, &v.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Version{}, fmt.Errorf("task %d version %d: %w", taskID, version, ErrNotFound)
	}

	if err != nil {
		return Version{}, fmt.Errorf("failed to get task version: %w", err)
	}

	return v, nil
}

// LatestFeedback returns the latest feedback of the kind on the task version.
func (r *repo) LatestFeedback(ctx context.Context, taskID int64, version int, kind FeedbackKind) (Feedback, error) {
	var f Feedback

	err := r.db.GetReadConnection().QueryRow(ctx, `
		SELECT id, task_id, version, kind, text, author_id, created_at
		FROM task_feedback
		WHERE task_id = $1 AND version = $2 AND kind = $3
		ORDER BY created_at DESC, id DESC
		LIMIT 1`, taskID, version, string(kind),
	).Scan(&f.ID, &f.TaskID, &f.Version, &f.Kind, &f.Text, &f.AuthorID, &f.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Feedback{}, fmt.Errorf("task %d version %d has no %s feedback: %w", taskID, version, kind, ErrNotFound)
	}

	if err != nil {
		return Feedback{}, fmt.Errorf("failed to get task feedback: %w", err)
	}

	return f, nil
}

// FindByDraftMessage finds the task version whose draft is the message in the chat.
func (r *repo) FindByDraftMessage(ctx context.Context, chatID, messageID int64) (taskID int64, version int, err error) {
	err = r.db.GetReadConnection().QueryRow(ctx, `
		SELECT v.task_id, v.version
		FROM task_versions v
		JOIN tasks t ON t.id = v.task_id
		WHERE v.draft_message_id = $2 AND v.draft_message_id > 0 AND t.source_chat_id = $1`, chatID, messageID,
	).Scan(&taskID, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, 0, fmt.Errorf("no draft with message %d in chat %d: %w", messageID, chatID, ErrNotFound)
	}

	if err != nil {
		return 0, 0, fmt.Errorf("failed to find task by draft message: %w", err)
	}

	return taskID, version, nil
}

// delivery is a claimed draft delivery: everything needed to send the draft.
type delivery struct {
	Task    Task
	Version int
	Content string
	Note    string
	Attempt int // this attempt, from 1
}

// ClaimDelivery claims the delivery of the latest draft of a task awaiting approval: only one sender
// (a worker or the sweeper) gets it. The claim counts the attempt and holds the draft for the lease;
// false when the draft is sent, not due, or not the latest one.
func (r *repo) ClaimDelivery(
	ctx context.Context,
	taskID int64,
	version int,
	now time.Time,
	lease time.Duration,
) (delivery, bool, error) {
	const claim = `
		UPDATE task_versions v
		SET delivery_attempts = v.delivery_attempts + 1, delivery_after = $4
		FROM tasks t
		WHERE v.task_id = $1 AND v.version = $2 AND t.id = v.task_id
		  AND t.status = 'awaiting_approval' AND t.version = v.version
		  AND v.draft_message_id IS NULL AND v.delivery_after <= $3
		RETURNING v.content, v.delivery_note, v.delivery_attempts`

	d := delivery{Version: version}

	err := r.db.GetWriteConnection().QueryRow(ctx, claim, taskID, version, now, now.Add(lease)).
		Scan(&d.Content, &d.Note, &d.Attempt)
	if errors.Is(err, pgx.ErrNoRows) {
		return delivery{}, false, nil
	}

	if err != nil {
		return delivery{}, false, fmt.Errorf("failed to claim draft delivery: %w", err)
	}

	d.Task, err = r.Get(ctx, taskID)
	if err != nil {
		return delivery{}, false, err
	}

	return d, true, nil
}

// DelayDelivery schedules the next delivery attempt of a draft that could not be sent.
func (r *repo) DelayDelivery(ctx context.Context, taskID int64, version int, next time.Time) error {
	_, err := r.db.GetWriteConnection().Exec(ctx,
		`UPDATE task_versions SET delivery_after = $3 WHERE task_id = $1 AND version = $2 AND draft_message_id IS NULL`,
		taskID, version, next)
	if err != nil {
		return fmt.Errorf("failed to delay draft delivery: %w", err)
	}

	return nil
}

// SetDraftMessageID stores the message of the sent draft; false when another sender stored one first.
func (r *repo) SetDraftMessageID(ctx context.Context, taskID int64, version int, messageID int64) (bool, error) {
	tag, err := r.db.GetWriteConnection().Exec(ctx,
		`UPDATE task_versions SET draft_message_id = $3 WHERE task_id = $1 AND version = $2 AND draft_message_id IS NULL`,
		taskID, version, messageID)
	if err != nil {
		return false, fmt.Errorf("failed to set draft message id: %w", err)
	}

	return tag.RowsAffected() > 0, nil
}

// AddOldDraftMessage remembers a message of the version that is not its draft any more (a duplicate).
func (r *repo) AddOldDraftMessage(ctx context.Context, taskID int64, version int, messageID int64) error {
	_, err := r.db.GetWriteConnection().Exec(ctx, `
		UPDATE task_versions SET old_draft_message_ids = array_append(old_draft_message_ids, $3::bigint)
		WHERE task_id = $1 AND version = $2`, taskID, version, messageID)
	if err != nil {
		return fmt.Errorf("failed to remember old draft message: %w", err)
	}

	return nil
}

// IsOldDraftMessage reports whether the message in the chat was a draft that has been replaced.
func (r *repo) IsOldDraftMessage(ctx context.Context, chatID, messageID int64) (bool, error) {
	var found bool

	err := r.db.GetReadConnection().QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM task_versions v
			JOIN tasks t ON t.id = v.task_id
			WHERE v.old_draft_message_ids @> ARRAY[$2::bigint] AND t.source_chat_id = $1
		)`, chatID, messageID).Scan(&found)
	if err != nil {
		return false, fmt.Errorf("failed to find old draft message: %w", err)
	}

	return found, nil
}

func (r *repo) CountByStatus(ctx context.Context) (map[Status]int64, error) {
	rows, err := r.db.GetReadConnection().Query(ctx, `SELECT status, count(*) FROM tasks GROUP BY status`)
	if err != nil {
		return nil, fmt.Errorf("failed to count tasks: %w", err)
	}
	defer rows.Close()

	counts := make(map[Status]int64, len(Statuses))

	for rows.Next() {
		var (
			status Status
			count  int64
		)

		if err := rows.Scan(&status, &count); err != nil {
			return nil, fmt.Errorf("failed to scan task count: %w", err)
		}

		counts[status] = count
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to read task counts: %w", err)
	}

	return counts, nil
}

// ClaimLost marks as queued now and returns the tasks the queue may have lost: queued ones that are due and
// were last queued before queuedBefore, and running ones started before staleBefore (their worker died)
// that were not queued again since then. Marking them keeps the sweeper from queueing them every time.
func (r *repo) ClaimLost(ctx context.Context, now, queuedBefore, staleBefore time.Time) ([]int64, error) {
	rows, err := r.db.GetWriteConnection().Query(ctx, `
		UPDATE tasks SET enqueued_at = $1
		WHERE id IN (
			SELECT id FROM tasks
			WHERE (status = 'queued' AND run_after <= $1 AND enqueued_at <= $2)
			   OR (status = 'running' AND started_at <= $3 AND enqueued_at <= $3)
			ORDER BY id
			LIMIT $4
			FOR UPDATE SKIP LOCKED
		)
		RETURNING id`, now, queuedBefore, staleBefore, sweepBatch)
	if err != nil {
		return nil, fmt.Errorf("failed to claim lost tasks: %w", err)
	}

	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return nil, fmt.Errorf("failed to read lost tasks: %w", err)
	}

	return ids, nil
}

// undelivered is a draft version that was not sent yet.
type undelivered struct {
	TaskID  int64
	Version int
}

// FindUndelivered returns the latest drafts of tasks awaiting approval that were never sent and whose
// next delivery attempt is due.
func (r *repo) FindUndelivered(ctx context.Context, now time.Time) ([]undelivered, error) {
	rows, err := r.db.GetReadConnection().Query(ctx, `
		SELECT v.task_id, v.version
		FROM task_versions v
		JOIN tasks t ON t.id = v.task_id AND t.version = v.version
		WHERE v.draft_message_id IS NULL AND v.delivery_after <= $1 AND t.status = 'awaiting_approval'
		ORDER BY v.task_id
		LIMIT $2`, now, sweepBatch)
	if err != nil {
		return nil, fmt.Errorf("failed to find undelivered drafts: %w", err)
	}

	found, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (undelivered, error) {
		var u undelivered
		err := row.Scan(&u.TaskID, &u.Version)

		return u, err
	})
	if err != nil {
		return nil, fmt.Errorf("failed to read undelivered drafts: %w", err)
	}

	return found, nil
}
