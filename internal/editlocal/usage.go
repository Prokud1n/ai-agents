package editlocal

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/Prokud1n/ai-agents/internal/agents"
	"github.com/Prokud1n/ai-agents/internal/llmusage"
)

type agentRunner interface {
	Run(ctx context.Context, data *agents.RunAgentData) (*agents.Result, error)
}

type pricer interface {
	Cost(model string, t llmusage.Tokens) (int64, bool)
}

// Usage is what the editor's model took during this run: the API calls, their cost (llm_usage records every call as
// in the app) and the time the decisions took.
type Usage struct {
	Calls int
	// UnpricedCalls: calls of a model without a price in LLM_PRICES (their cost is not in CostNano).
	UnpricedCalls int
	CostNano      int64
	ModelTime     time.Duration
}

// Summary is one line for the owner: the calls and the cost.
func (u Usage) Summary() string {
	summary := fmt.Sprintf("вызовов API %d, $%s", u.Calls, llmusage.FormatUSD(u.CostNano))
	if u.UnpricedCalls > 0 {
		summary += fmt.Sprintf(" (ещё %d без цены в LLM_PRICES)", u.UnpricedCalls)
	}

	return summary
}

// UsageMeter is the runner of the editor's decider that counts the calls, the cost and the time of every run of the
// model; the runs themselves go to agents.Runtime unchanged (it records them in llm_usage).
type UsageMeter struct {
	runner agentRunner
	prices pricer
	now    func() time.Time

	mu    sync.Mutex
	usage Usage
}

func NewUsageMeter(runner agentRunner, prices pricer) *UsageMeter {
	return &UsageMeter{runner: runner, prices: prices, now: time.Now}
}

// Run runs the agent and counts its result. The error is returned as it is: the editor tells its kinds apart.
func (m *UsageMeter) Run(ctx context.Context, data *agents.RunAgentData) (*agents.Result, error) {
	start := m.now()
	result, err := m.runner.Run(ctx, data)
	m.add(result, m.now().Sub(start))

	// Not wrapped: a transparent proxy, the decider and the editor match the runtime's errors.
	return result, err
}

func (m *UsageMeter) add(result *agents.Result, took time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.usage.ModelTime += took

	// A run that failed without a result made no call that could be counted here; llm_usage has what was billed.
	if result == nil {
		return
	}

	m.usage.Calls += result.Calls

	cost, ok := m.prices.Cost(result.Model, result.Tokens)
	if !ok {
		m.usage.UnpricedCalls += result.Calls

		return
	}

	m.usage.CostNano += cost
}

// Snapshot is the usage so far.
func (m *UsageMeter) Snapshot() Usage {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.usage
}
