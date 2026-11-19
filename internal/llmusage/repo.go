package llmusage

import (
	"context"
	"fmt"
	"time"

	"github.com/Prokud1n/ai-agents/internal/providers/postgres"
)

type repo struct {
	db *postgres.Client
}

func newRepo(db *postgres.Client) *repo {
	return &repo{db: db}
}

// insert passes the cost as text and casts it in SQL: numeric gets the exact decimal, NULL stays NULL.
func (r *repo) insert(ctx context.Context, e *Entry) error {
	var cost *string
	if e.CostNano != nil {
		formatted := FormatUSD(*e.CostNano)
		cost = &formatted
	}

	_, err := r.db.GetWriteConnection().Exec(ctx, `
		INSERT INTO llm_usage (
			agent, model, response_model, request_id, input_tokens, output_tokens, cache_read_tokens,
			cache_write_tokens, cost_usd, duration_ms, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::text::numeric, $10, $11)`,
		e.Agent, e.Model, e.ResponseModel, e.RequestID, e.Tokens.Input, e.Tokens.Output, e.Tokens.CacheRead,
		e.Tokens.CacheWrite, cost, e.Duration.Milliseconds(), e.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to insert llm usage: %w", err)
	}

	return nil
}

func (r *repo) summary(ctx context.Context, since time.Time) ([]SummaryRow, error) {
	rows, err := r.db.GetReadConnection().Query(ctx, `
		SELECT agent, model, COUNT(*),
			COALESCE(SUM(input_tokens), 0)::bigint,
			COALESCE(SUM(output_tokens), 0)::bigint,
			COALESCE(SUM(cache_read_tokens), 0)::bigint,
			COALESCE(SUM(cache_write_tokens), 0)::bigint,
			COALESCE(SUM(cost_usd), 0)::text,
			COUNT(*) FILTER (WHERE cost_usd IS NULL)
		FROM llm_usage
		WHERE created_at >= $1
		GROUP BY agent, model
		ORDER BY agent, model`,
		since,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query llm usage: %w", err)
	}
	defer rows.Close()

	var result []SummaryRow

	for rows.Next() {
		var (
			row  SummaryRow
			cost string
		)

		if err := rows.Scan(
			&row.Agent, &row.Model, &row.Calls,
			&row.Tokens.Input, &row.Tokens.Output, &row.Tokens.CacheRead, &row.Tokens.CacheWrite,
			&cost, &row.UnpricedCalls,
		); err != nil {
			return nil, fmt.Errorf("failed to scan llm usage: %w", err)
		}

		costNano, err := parseUSD(cost)
		if err != nil {
			return nil, err
		}

		row.CostNano = costNano

		result = append(result, row)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to read llm usage: %w", err)
	}

	return result, nil
}
