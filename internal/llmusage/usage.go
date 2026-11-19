package llmusage

import "time"

// Entry is the usage of one Claude API call.
type Entry struct {
	Agent string
	// Model is the requested model (the price is looked up by it); ResponseModel is from the response.
	Model         string
	ResponseModel string
	RequestID     string
	Tokens        Tokens
	// CostNano is the cost in nano-dollars; nil when the model has no price.
	CostNano  *int64
	Duration  time.Duration
	CreatedAt time.Time
}

// SummaryRow is the usage of one agent with one (requested) model over a period.
type SummaryRow struct {
	Agent  string
	Model  string
	Calls  int64
	Tokens Tokens
	// CostNano sums priced calls only; UnpricedCalls counts calls whose model had no price.
	CostNano      int64
	UnpricedCalls int64
}

// CostUSD formats the cost in dollars.
func (r *SummaryRow) CostUSD() string {
	return FormatUSD(r.CostNano)
}

// Summary is the usage since a moment: rows by agent and model, ordered by agent then model, and the total.
type Summary struct {
	Since time.Time
	Rows  []SummaryRow
	Total SummaryRow
}

// summarize builds a summary; Total has empty Agent and Model.
func summarize(since time.Time, rows []SummaryRow) Summary {
	summary := Summary{Since: since, Rows: rows}

	for i := range rows {
		summary.Total.Calls += rows[i].Calls
		summary.Total.Tokens = summary.Total.Tokens.Add(rows[i].Tokens)
		summary.Total.CostNano += rows[i].CostNano
		summary.Total.UnpricedCalls += rows[i].UnpricedCalls
	}

	return summary
}
