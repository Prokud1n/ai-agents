package llmusage

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/Prokud1n/ai-agents/internal/config"
)

type Service struct {
	repo   *repo
	prices Prices
	log    *slog.Logger
}

// NewService fails on malformed LLM_PRICES, so a typo in prices stops the start instead of losing costs.
func NewService(cfg *config.LLM, r *repo, log *slog.Logger) (*Service, error) {
	prices, err := ParsePrices(cfg.Prices)
	if err != nil {
		return nil, fmt.Errorf("failed to parse LLM_PRICES: %w", err)
	}

	return &Service{repo: r, prices: prices, log: log}, nil
}

// Cost returns the cost of tokens in nano-dollars; false if the model has no price.
func (s *Service) Cost(model string, t Tokens) (int64, bool) {
	return s.prices.Cost(model, t)
}

// Record stores the usage of one API call. A model without a price is stored with a NULL cost.
func (s *Service) Record(ctx context.Context, data *RecordUsageData) error {
	entry := newEntry(s.prices, data)

	if entry.CostNano == nil {
		s.log.WarnContext(ctx, "llmusage: no price for model, cost is not recorded", "model", data.Model)
	}

	if err := s.repo.insert(ctx, &entry); err != nil {
		return fmt.Errorf("failed to record llm usage: %w", err)
	}

	return nil
}

// UsageSummary returns usage by agent and model since the given moment.
func (s *Service) UsageSummary(ctx context.Context, since time.Time) (Summary, error) {
	rows, err := s.repo.summary(ctx, since)
	if err != nil {
		return Summary{}, fmt.Errorf("failed to get llm usage summary: %w", err)
	}

	return summarize(since, rows), nil
}

func newEntry(prices Prices, data *RecordUsageData) Entry {
	entry := Entry{
		Agent:     data.Agent,
		Model:     data.Model,
		RequestID: data.RequestID,
		Tokens:    data.Tokens,
		Duration:  data.Duration,
		CreatedAt: data.CreatedAt,
	}

	if cost, ok := prices.Cost(data.Model, data.Tokens); ok {
		entry.CostNano = &cost
	}

	return entry
}
