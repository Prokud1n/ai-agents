package llmusage

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	sonnet       = "claude-sonnet-5-5"
	unknownModel = "claude-unknown"
	bigCostUSD   = "3.125000000"
)

func TestParsePrices(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     string
		expected  Prices
		expectErr bool
	}{
		{name: "empty", input: "", expected: Prices{}},
		{
			name:  "official defaults",
			input: "claude-sonnet-5-5=2/10/2.5/0.1,claude-opus-5-5=4/20/5/0.2",
			expected: Prices{
				sonnet:            {Input: 2000, Output: 10000, CacheWrite: 2500, CacheRead: 100},
				"claude-opus-5-5": {Input: 4000, Output: 20000, CacheWrite: 5000, CacheRead: 200},
			},
		},
		{
			name:     "spaces and trailing comma",
			input:    " m = 0.125 / 0.5 / 0.01 / 0 , ",
			expected: Prices{"m": {Input: 125, Output: 500, CacheWrite: 10, CacheRead: 0}},
		},
		{name: "missing model", input: "=1/2/3/4", expectErr: true},
		{name: "missing equals", input: "m", expectErr: true},
		{name: "three values", input: "m=1/2/3", expectErr: true},
		{name: "too many fraction digits", input: "m=0.0001/1/1/1", expectErr: true},
		{name: "negative", input: "m=-1/1/1/1", expectErr: true},
		{name: "not a number", input: "m=a/1/1/1", expectErr: true},
		{name: "empty value", input: "m=/1/1/1", expectErr: true},
		{name: "duplicate model", input: "m=1/1/1/1,m=2/2/2/2", expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParsePrices(tt.input)
			if tt.expectErr {
				require.ErrorIs(t, err, errInvalidPrice)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestPricesCost(t *testing.T) {
	t.Parallel()

	prices, err := ParsePrices("claude-sonnet-5-5=2/10/2.5/0.1")
	require.NoError(t, err)

	tests := []struct {
		name         string
		model        string
		tokens       Tokens
		expectedNano int64
		expectedUSD  string
		expectedOK   bool
	}{
		{
			name:  "all token kinds",
			model: sonnet,
			// 2*1M + 10*100k + 2.5*10k + 0.1*1M = 2 + 1 + 0.025 + 0.1 USD
			tokens:       Tokens{Input: 1_000_000, Output: 100_000, CacheWrite: 10_000, CacheRead: 1_000_000},
			expectedNano: 3_125_000_000,
			expectedUSD:  bigCostUSD,
			expectedOK:   true,
		},
		{
			name:         "one output token",
			model:        sonnet,
			tokens:       Tokens{Output: 1},
			expectedNano: 10_000,
			expectedUSD:  "0.000010000",
			expectedOK:   true,
		},
		{name: "no tokens", model: sonnet, expectedUSD: "0.000000000", expectedOK: true},
		{name: "unknown model", model: unknownModel, tokens: Tokens{Input: 1}, expectedUSD: "0.000000000"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			nano, ok := prices.Cost(tt.model, tt.tokens)

			assert.Equal(t, tt.expectedOK, ok)
			assert.Equal(t, tt.expectedNano, nano)
			assert.Equal(t, tt.expectedUSD, FormatUSD(nano))
		})
	}
}

func TestParseUSD(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     string
		expected  int64
		expectErr bool
	}{
		{name: "zero", input: "0", expected: 0},
		{name: "numeric text", input: bigCostUSD, expected: 3_125_000_000},
		{name: "short fraction", input: "0.5", expected: 500_000_000},
		{name: "too precise", input: "0.0000000001", expectErr: true},
		{name: "garbage", input: "abc", expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseUSD(tt.input)
			if tt.expectErr {
				require.Error(t, err)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expected, got)
			assert.Equal(t, tt.expected, mustParseUSD(t, FormatUSD(got)))
		})
	}
}

func mustParseUSD(t *testing.T, s string) int64 {
	t.Helper()

	v, err := parseUSD(s)
	require.NoError(t, err)

	return v
}

func TestNewEntry(t *testing.T) {
	t.Parallel()

	prices := Prices{sonnet: {Input: 2000, Output: 10000}}
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name         string
		model        string
		expectedCost *int64
	}{
		{name: "priced model", model: sonnet, expectedCost: new(int64(30_000))},
		{name: "unpriced model has no cost", model: unknownModel},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			entry := newEntry(prices, &RecordUsageData{
				Agent: "a", Model: tt.model, ResponseModel: "claude-dated-20261101", RequestID: "req",
				Tokens: Tokens{Input: 10, Output: 1}, Duration: time.Second, CreatedAt: now,
			})

			// The price is looked up by the requested model, the response model is kept for reference.
			assert.Equal(t, tt.expectedCost, entry.CostNano)
			assert.Equal(t, tt.model, entry.Model)
			assert.Equal(t, "claude-dated-20261101", entry.ResponseModel)
			assert.Equal(t, now, entry.CreatedAt)
			assert.Equal(t, Tokens{Input: 10, Output: 1}, entry.Tokens)
		})
	}
}

func TestSummarize(t *testing.T) {
	t.Parallel()

	since := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name     string
		rows     []SummaryRow
		expected SummaryRow
	}{
		{name: "no usage", rows: nil, expected: SummaryRow{}},
		{
			name: "totals over agents and models",
			rows: []SummaryRow{
				{Agent: copywriter, Model: sonnet, Calls: 2, Tokens: Tokens{Input: 10, Output: 5}, CostNano: 70},
				{Agent: marketer, Model: sonnet, Calls: 1, Tokens: Tokens{CacheRead: 3, CacheWrite: 4}, CostNano: 30},
				{Agent: marketer, Model: "custom", Calls: 4, Tokens: Tokens{Input: 1}, UnpricedCalls: 4},
			},
			expected: SummaryRow{
				Calls:         7,
				Tokens:        Tokens{Input: 11, Output: 5, CacheRead: 3, CacheWrite: 4},
				CostNano:      100,
				UnpricedCalls: 4,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := summarize(since, tt.rows)

			assert.Equal(t, since, got.Since)
			assert.Equal(t, tt.rows, got.Rows)
			assert.Equal(t, tt.expected, got.Total)
			assert.Equal(t, FormatUSD(tt.expected.CostNano), got.Total.CostUSD())
		})
	}
}

func TestServiceHasPrice(t *testing.T) {
	t.Parallel()

	service := &Service{prices: Prices{sonnet: {Input: 1}}}

	tests := []struct {
		name     string
		model    string
		expected bool
	}{
		{name: "priced", model: sonnet, expected: true},
		{name: "unpriced", model: unknownModel, expected: false},
		{name: "empty", model: "", expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, service.HasPrice(tt.model))
		})
	}
}
