package pilot

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Prokud1n/ai-agents/internal/providers/postgres"
)

const feedbackColumns = `id, author_id, author_name, text, truncated, task_id, task_version, topic, topic_id,
	source_chat_id, source_message_id, created_at`

type repo struct {
	db *postgres.Client
}

func newRepo(db *postgres.Client) *repo {
	return &repo{db: db}
}

func scanFeedback(row pgx.Row) (Feedback, error) {
	var (
		f       Feedback
		taskID  *int64
		version *int
	)

	err := row.Scan(&f.ID, &f.AuthorID, &f.AuthorName, &f.Text, &f.Truncated, &taskID, &version, &f.Topic, &f.TopicID,
		&f.ChatID, &f.MessageID, &f.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Feedback{}, ErrNotFound
	}

	if err != nil {
		return Feedback{}, fmt.Errorf("failed to scan feedback: %w", err)
	}

	if taskID != nil && version != nil {
		f.TaskID, f.TaskVersion = *taskID, *version
	}

	return f, nil
}

// SaveFeedback inserts the feedback; the same source message again returns the saved one with created = false.
func (r *repo) SaveFeedback(ctx context.Context, data *SaveFeedbackData) (feedback Feedback, created bool, err error) {
	var (
		taskID  *int64
		version *int
	)

	if data.TaskID > 0 && data.TaskVersion > 0 {
		taskID, version = new(data.TaskID), new(data.TaskVersion)
	}

	feedback, err = scanFeedback(r.db.GetWriteConnection().QueryRow(ctx, `
		INSERT INTO feedback (author_id, author_name, text, truncated, task_id, task_version, topic, topic_id,
			source_chat_id, source_message_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (source_chat_id, source_message_id) DO NOTHING
		RETURNING `+feedbackColumns,
		data.AuthorID, data.AuthorName, data.Text, data.Truncated, taskID, version, data.Topic, data.TopicID,
		data.ChatID, data.MessageID))
	if err == nil {
		return feedback, true, nil
	}

	if !errors.Is(err, ErrNotFound) {
		return Feedback{}, false, fmt.Errorf("failed to insert feedback: %w", err)
	}

	feedback, err = scanFeedback(r.db.GetWriteConnection().QueryRow(ctx,
		`SELECT `+feedbackColumns+` FROM feedback WHERE source_chat_id = $1 AND source_message_id = $2`,
		data.ChatID, data.MessageID))
	if err != nil {
		return Feedback{}, false, fmt.Errorf("failed to find saved feedback: %w", err)
	}

	return feedback, false, nil
}

// ListFeedback returns the feedback of the period, the oldest first, at most limit, and how many there are in all.
func (r *repo) ListFeedback(ctx context.Context, since, until time.Time, limit int) ([]Feedback, int, error) {
	total, err := r.CountFeedback(ctx, since, until)
	if err != nil {
		return nil, 0, err
	}

	rows, err := r.db.GetReadConnection().Query(ctx,
		`SELECT `+feedbackColumns+` FROM feedback WHERE created_at >= $1 AND created_at < $2
		ORDER BY created_at, id LIMIT $3`, since, until, limit)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list feedback: %w", err)
	}
	defer rows.Close()

	list := make([]Feedback, 0, min(total, limit))

	for rows.Next() {
		f, err := scanFeedback(rows)
		if err != nil {
			return nil, 0, err
		}

		list = append(list, f)
	}

	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("failed to read feedback: %w", err)
	}

	return list, total, nil
}

func (r *repo) CountFeedback(ctx context.Context, since, until time.Time) (int, error) {
	var count int

	err := r.db.GetReadConnection().QueryRow(ctx,
		`SELECT count(*) FROM feedback WHERE created_at >= $1 AND created_at < $2`, since, until).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("failed to count feedback: %w", err)
	}

	return count, nil
}

// TaskRows returns the tasks created in the period with the time of their first version.
func (r *repo) TaskRows(ctx context.Context, since, until time.Time) ([]TaskRow, error) {
	rows, err := r.db.GetReadConnection().Query(ctx, `
		SELECT t.agent, t.kind, t.status, t.version,
			(SELECT count(*) FROM task_feedback f WHERE f.task_id = t.id AND f.kind = 'revise'),
			t.created_at, v.created_at
		FROM tasks t
		LEFT JOIN task_versions v ON v.task_id = t.id AND v.version = 1
		WHERE t.created_at >= $1 AND t.created_at < $2
		ORDER BY t.id`, since, until)
	if err != nil {
		return nil, fmt.Errorf("failed to list tasks of the period: %w", err)
	}
	defer rows.Close()

	var list []TaskRow

	for rows.Next() {
		var row TaskRow
		if err := rows.Scan(&row.Agent, &row.Kind, &row.Status, &row.Version, &row.Revisions, &row.CreatedAt,
			&row.FirstDraftAt); err != nil {
			return nil, fmt.Errorf("failed to scan task of the period: %w", err)
		}

		list = append(list, row)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to read tasks of the period: %w", err)
	}

	return list, nil
}

// ClaimReport marks the week's report as sent; false when it is marked already.
func (r *repo) ClaimReport(ctx context.Context, week time.Time) (bool, error) {
	tag, err := r.db.GetWriteConnection().Exec(ctx,
		`INSERT INTO pilot_reports (week) VALUES ($1) ON CONFLICT (week) DO NOTHING`, week)
	if err != nil {
		return false, fmt.Errorf("failed to claim pilot report: %w", err)
	}

	return tag.RowsAffected() == 1, nil
}

// ReleaseReport removes the mark of a report that was not sent, so it is tried again.
func (r *repo) ReleaseReport(ctx context.Context, week time.Time) error {
	if _, err := r.db.GetWriteConnection().Exec(ctx, `DELETE FROM pilot_reports WHERE week = $1`, week); err != nil {
		return fmt.Errorf("failed to release pilot report: %w", err)
	}

	return nil
}

func (r *repo) OnboardingMessage(ctx context.Context, chatID int64, topic string) (OnboardingMessage, error) {
	msg := OnboardingMessage{ChatID: chatID, Topic: topic}

	err := r.db.GetReadConnection().QueryRow(ctx,
		`SELECT topic_id, role, message_id, pinned FROM onboarding_messages WHERE chat_id = $1 AND topic = $2`,
		chatID, topic,
	).Scan(&msg.TopicID, &msg.Role, &msg.MessageID, &msg.Pinned)
	if errors.Is(err, pgx.ErrNoRows) {
		return OnboardingMessage{}, fmt.Errorf("greeting of topic %s: %w", topic, ErrNotFound)
	}

	if err != nil {
		return OnboardingMessage{}, fmt.Errorf("failed to get greeting: %w", err)
	}

	return msg, nil
}

func (r *repo) SaveOnboardingMessage(ctx context.Context, msg *OnboardingMessage) error {
	_, err := r.db.GetWriteConnection().Exec(ctx, `
		INSERT INTO onboarding_messages (chat_id, topic, topic_id, role, message_id, pinned)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (chat_id, topic) DO UPDATE
		SET topic_id = $3, role = $4, message_id = $5, pinned = $6, updated_at = now()`,
		msg.ChatID, msg.Topic, msg.TopicID, msg.Role, msg.MessageID, msg.Pinned)
	if err != nil {
		return fmt.Errorf("failed to save greeting: %w", err)
	}

	return nil
}
