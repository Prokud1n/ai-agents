package llmusage

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/pgtest"
)

const (
	usageTable   = "llm_usage"
	customModel  = "claude-custom"
	datedModel   = "claude-sonnet-5-5-20261101"
	copywriter   = "copywriter"
	marketer     = "marketer"
	integPrices  = "claude-sonnet-5-5=2/10/2.5/0.1"
	bigCostNano  = 3_125_000_000 // 2*1M + 10*100k + 2.5*10k + 0.1*1M tokens in USD per MTok = $3.125
	tinyCostNano = 10_000        // one output token: $10 / 1M
)

// TestIntegrationUsage records usage into a real llm_usage table and checks exact sums.
// Not parallel: it owns the llm_usage table.
func TestIntegrationUsage(t *testing.T) {
	client := pgtest.Connect(t)
	pgtest.Truncate(t, client, usageTable)

	service, err := NewService(&config.LLM{Prices: integPrices}, newRepo(client), slog.New(slog.DiscardHandler))
	require.NoError(t, err)

	ctx := context.Background()
	since := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

	records := []RecordUsageData{
		{ // before the period: not in the summary
			Agent: copywriter, Model: sonnet, Tokens: Tokens{Input: 999},
			CreatedAt: since.Add(-time.Second),
		},
		{
			Agent: copywriter, Model: sonnet, ResponseModel: datedModel, RequestID: "req_1",
			Tokens:   Tokens{Input: 1_000_000, Output: 100_000, CacheWrite: 10_000, CacheRead: 1_000_000},
			Duration: 1500 * time.Millisecond, CreatedAt: since,
		},
		{
			Agent: copywriter, Model: sonnet, ResponseModel: datedModel, RequestID: "req_2",
			Tokens: Tokens{Output: 1}, CreatedAt: since.Add(time.Hour),
		},
		{
			Agent: marketer, Model: customModel, ResponseModel: customModel, RequestID: "req_3",
			Tokens: Tokens{Input: 5, Output: 7}, CreatedAt: since.Add(time.Hour),
		},
		{
			Agent: marketer, Model: customModel, RequestID: "req_4",
			Tokens: Tokens{CacheRead: 11, CacheWrite: 13}, CreatedAt: since.Add(2 * time.Hour),
		},
	}

	for i := range records {
		require.NoError(t, service.Record(ctx, &records[i]))
	}

	summary, err := service.UsageSummary(ctx, since)
	require.NoError(t, err)

	expectedRows := []SummaryRow{
		{
			Agent: copywriter, Model: sonnet, Calls: 2,
			Tokens:   Tokens{Input: 1_000_000, Output: 100_001, CacheWrite: 10_000, CacheRead: 1_000_000},
			CostNano: bigCostNano + tinyCostNano,
		},
		{
			Agent: marketer, Model: customModel, Calls: 2,
			Tokens:        Tokens{Input: 5, Output: 7, CacheRead: 11, CacheWrite: 13},
			UnpricedCalls: 2,
		},
	}

	assert.Equal(t, since, summary.Since)
	assert.Equal(t, expectedRows, summary.Rows)
	assert.Equal(t, int64(4), summary.Total.Calls)
	assert.Equal(t, "3.125010000", summary.Total.CostUSD())
	assert.Equal(t, int64(2), summary.Total.UnpricedCalls)

	assertStoredRow(t, client.GetReadConnection(), "req_1", datedModel, new(bigCostUSD), 1500)
	assertStoredRow(t, client.GetReadConnection(), "req_3", customModel, nil, 0)
}

func assertStoredRow(
	t *testing.T,
	db *pgxpool.Pool,
	requestID, expectedResponseModel string,
	expectedCost *string,
	expectedDurationMS int64,
) {
	t.Helper()

	var (
		responseModel string
		cost          *string
		durationMS    int64
	)

	err := db.QueryRow(context.Background(),
		`SELECT response_model, cost_usd::text, duration_ms FROM llm_usage WHERE request_id = $1`, requestID,
	).Scan(&responseModel, &cost, &durationMS)
	require.NoError(t, err)

	assert.Equal(t, expectedResponseModel, responseModel)
	assert.Equal(t, expectedCost, cost)
	assert.Equal(t, expectedDurationMS, durationMS)
}
