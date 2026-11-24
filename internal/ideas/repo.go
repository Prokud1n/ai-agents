package ideas

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Prokud1n/ai-agents/internal/providers/postgres"
)

type repo struct {
	db *postgres.Client
}

func newRepo(db *postgres.Client) *repo {
	return &repo{db: db}
}

const ideaColumns = `id, task_id, task_version, title, summary, format, format_note, hook, source_text, status,
	created_at, updated_at`

const selectIdea = `SELECT ` + ideaColumns + ` FROM ideas`

// insertApproved makes ideas of the approved idea tasks that have none: the card of the approved (latest) version,
// the task's input as the trainer's words. The tasks are read from the tasks table: an idea exists only for a task
// approved there, and only of the card whose content is the approved draft (task_versions), so a card written by a
// stale run of the version never reaches the backlog. $1 is the time, $2 the task (NULL: every such task).
const insertApproved = `
	INSERT INTO ideas (task_id, task_version, title, summary, format, format_note, hook, source_text, status,
	                   created_at, updated_at)
	SELECT c.task_id, c.version, c.title, c.summary, c.format, c.format_note, c.hook, t.input, 'new', $1, $1
	FROM tasks t
	JOIN idea_cards c ON c.task_id = t.id AND c.version = t.version
	JOIN task_versions v ON v.task_id = c.task_id AND v.version = c.version AND v.content = c.content
	WHERE t.kind = '` + TaskKind + `' AND t.status = 'approved'
	  AND ($2::bigint IS NULL OR t.id = $2)
	  AND NOT EXISTS (SELECT 1 FROM ideas i WHERE i.task_id = t.id)
	ORDER BY t.updated_at, t.id
	ON CONFLICT (task_id) DO NOTHING`

func scanIdea(row pgx.Row) (Idea, error) {
	var i Idea

	err := row.Scan(&i.ID, &i.TaskID, &i.TaskVersion, &i.Card.Title, &i.Card.Summary, &i.Card.Format,
		&i.Card.FormatNote, &i.Card.Hook, &i.SourceText, &i.Status, &i.CreatedAt, &i.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Idea{}, ErrNotFound
	}

	if err != nil {
		return Idea{}, fmt.Errorf("failed to scan idea: %w", err)
	}

	return i, nil
}

func scanIdeas(rows pgx.Rows) ([]Idea, error) {
	defer rows.Close()

	var list []Idea

	for rows.Next() {
		i, err := scanIdea(rows)
		if err != nil {
			return nil, err
		}

		list = append(list, i)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to read ideas: %w", err)
	}

	return list, nil
}

// SaveCard stores the card of a version and its draft text; a rerun of the version replaces it while the version is
// not stored yet. False: the version is already in task_versions (this run is stale), the card is left as it is.
func (r *repo) SaveCard(
	ctx context.Context,
	taskID int64,
	version int,
	card *Card,
	content string,
	now time.Time,
) (bool, error) {
	const upsert = `
		INSERT INTO idea_cards (task_id, version, title, summary, format, format_note, hook, content, created_at,
		                        updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $9)
		ON CONFLICT (task_id, version) DO UPDATE
		SET title = EXCLUDED.title, summary = EXCLUDED.summary, format = EXCLUDED.format,
		    format_note = EXCLUDED.format_note, hook = EXCLUDED.hook, content = EXCLUDED.content,
		    updated_at = EXCLUDED.updated_at
		WHERE NOT EXISTS (
			SELECT 1 FROM task_versions v WHERE v.task_id = idea_cards.task_id AND v.version = idea_cards.version
		)`

	tag, err := r.db.GetWriteConnection().Exec(ctx, upsert, taskID, version, card.Title, card.Summary,
		string(card.Format), card.FormatNote, card.Hook, content, now)
	if err != nil {
		return false, fmt.Errorf("failed to save idea card of task %d version %d: %w", taskID, version, err)
	}

	return tag.RowsAffected() > 0, nil
}

func (r *repo) GetCard(ctx context.Context, taskID int64, version int) (Card, error) {
	var c Card

	err := r.db.GetReadConnection().QueryRow(ctx, `
		SELECT title, summary, format, format_note, hook FROM idea_cards WHERE task_id = $1 AND version = $2`,
		taskID, version).Scan(&c.Title, &c.Summary, &c.Format, &c.FormatNote, &c.Hook)
	if errors.Is(err, pgx.ErrNoRows) {
		return Card{}, fmt.Errorf("card of task %d version %d: %w", taskID, version, ErrNotFound)
	}

	if err != nil {
		return Card{}, fmt.Errorf("failed to get idea card of task %d version %d: %w", taskID, version, err)
	}

	return c, nil
}

func (r *repo) GetByTask(ctx context.Context, taskID int64) (Idea, error) {
	i, err := scanIdea(r.db.GetReadConnection().QueryRow(ctx, selectIdea+` WHERE task_id = $1`, taskID))
	if err != nil {
		return Idea{}, fmt.Errorf("failed to get idea of task %d: %w", taskID, err)
	}

	return i, nil
}

// InsertApproved makes the idea of an approved task; false when the task already has one (or is not an approved idea
// task with a card: the caller tells these apart).
func (r *repo) InsertApproved(ctx context.Context, taskID int64, now time.Time) (bool, error) {
	tag, err := r.db.GetWriteConnection().Exec(ctx, insertApproved, now, taskID)
	if err != nil {
		return false, fmt.Errorf("failed to insert idea of task %d: %w", taskID, err)
	}

	return tag.RowsAffected() > 0, nil
}

// CardMismatch reports whether the task is an approved idea task whose card of the approved version is not the
// approved draft (the card of a stale run): such a card is not put into the backlog.
func (r *repo) CardMismatch(ctx context.Context, taskID int64) (bool, error) {
	var mismatch bool

	err := r.db.GetReadConnection().QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM tasks t
			JOIN idea_cards c ON c.task_id = t.id AND c.version = t.version
			JOIN task_versions v ON v.task_id = c.task_id AND v.version = c.version
			WHERE t.id = $1 AND t.kind = '`+TaskKind+`' AND t.status = 'approved' AND v.content <> c.content
		)`, taskID).Scan(&mismatch)
	if err != nil {
		return false, fmt.Errorf("failed to check the idea card of task %d: %w", taskID, err)
	}

	return mismatch, nil
}

// SyncApproved makes the ideas of every approved idea task that has none (its save after the approval failed).
func (r *repo) SyncApproved(ctx context.Context, now time.Time) (int64, error) {
	tag, err := r.db.GetWriteConnection().Exec(ctx, insertApproved, now, nil)
	if err != nil {
		return 0, fmt.Errorf("failed to sync approved ideas: %w", err)
	}

	return tag.RowsAffected(), nil
}

// ListByStatus returns at most limit ideas in the status, the newest first or the oldest first, and how many there
// are in all.
func (r *repo) ListByStatus(ctx context.Context, status Status, limit int, newestFirst bool) ([]Idea, int, error) {
	order := ` ORDER BY id`
	if newestFirst {
		order += ` DESC`
	}

	conn := r.db.GetReadConnection()

	var total int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM ideas WHERE status = $1`, string(status)).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("failed to count ideas: %w", err)
	}

	rows, err := conn.Query(ctx, selectIdea+` WHERE status = $1`+order+` LIMIT $2`, string(status), limit)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list ideas: %w", err)
	}

	list, err := scanIdeas(rows)
	if err != nil {
		return nil, 0, err
	}

	return list, total, nil
}

// Update locks the idea, applies the updater and writes the change in one transaction. On an updater's error the
// idea as it is comes back with the error.
func (r *repo) Update(ctx context.Context, id int64, fn UpdaterFn) (Idea, error) {
	var current, updated Idea

	err := pgx.BeginFunc(ctx, r.db.GetWriteConnection(), func(tx pgx.Tx) error {
		var err error

		current, err = scanIdea(tx.QueryRow(ctx, selectIdea+` WHERE id = $1 FOR UPDATE`, id))
		if err != nil {
			return fmt.Errorf("failed to lock idea %d: %w", id, err)
		}

		updated, err = fn(current)
		if err != nil {
			return err
		}

		return writeIdea(ctx, tx, &updated)
	})
	if err != nil {
		return current, fmt.Errorf("failed to update idea: %w", err)
	}

	return updated, nil
}

// UpdateMany locks the ideas in the order of their numbers and applies the updater to each; ideas the updater refuses
// with a refusal (refused reports it) are left as they are, any other error rolls everything back. It returns the
// ideas that changed.
func (r *repo) UpdateMany(
	ctx context.Context,
	ids []int64,
	fn UpdaterFn,
	refused func(error) bool,
) ([]Idea, error) {
	var changed []Idea

	err := pgx.BeginFunc(ctx, r.db.GetWriteConnection(), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, selectIdea+` WHERE id = ANY($1) ORDER BY id FOR UPDATE`, ids)
		if err != nil {
			return fmt.Errorf("failed to lock ideas: %w", err)
		}

		locked, err := scanIdeas(rows)
		if err != nil {
			return err
		}

		changed, err = applyAll(ctx, tx, locked, fn, refused)

		return err
	})
	if err != nil {
		return nil, fmt.Errorf("failed to update ideas: %w", err)
	}

	return changed, nil
}

func applyAll(ctx context.Context, tx pgx.Tx, locked []Idea, fn UpdaterFn, refused func(error) bool) ([]Idea, error) {
	changed := make([]Idea, 0, len(locked))

	for i := range locked {
		current := locked[i]

		updated, err := fn(current)
		if err != nil && refused(err) {
			continue
		}

		if err != nil {
			return nil, err
		}

		if updated.Status == current.Status {
			continue
		}

		if err := writeIdea(ctx, tx, &updated); err != nil {
			return nil, err
		}

		changed = append(changed, updated)
	}

	return changed, nil
}

func writeIdea(ctx context.Context, tx pgx.Tx, i *Idea) error {
	if _, err := tx.Exec(ctx, `UPDATE ideas SET status = $2, updated_at = $3 WHERE id = $1`,
		i.ID, string(i.Status), i.UpdatedAt); err != nil {
		return fmt.Errorf("failed to write idea %d: %w", i.ID, err)
	}

	return nil
}
