package agents

import (
	"bytes"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/config"
)

// plainModel is the model of testdata/valid/plain.
const plainModel = "claude-test"

type fakePrices map[string]bool

func (p fakePrices) HasPrice(model string) bool {
	return p[model]
}

func TestNewRegistryWarnings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		dir         string
		prices      fakePrices
		expected    []string
		notExpected []string
	}{
		{
			name:   "agent model without a price",
			dir:    validAgentsDir,
			prices: fakePrices{plainModel: true},
			// coach uses the default model, which has no price; plain has its own priced model.
			expected:    []string{`"agent":"coach"`, `"model":"claude-default-test"`, "has no price"},
			notExpected: []string{`"agent":"plain"`, "no agents found"},
		},
		{
			name:     "no agents",
			dir:      t.TempDir(),
			prices:   fakePrices{},
			expected: []string{"no agents found"},
		},
		{
			name:        "all models priced",
			dir:         validAgentsDir,
			prices:      fakePrices{plainModel: true, testModel: true},
			notExpected: []string{"WARN"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var logs bytes.Buffer

			log := slog.New(slog.NewJSONHandler(&logs, nil))

			registry, err := NewRegistry(&config.Agents{Dir: tt.dir}, &config.LLM{DefaultModel: testModel},
				tt.prices, log, testTools())
			require.NoError(t, err)
			require.NotNil(t, registry)

			for _, s := range tt.expected {
				assert.Contains(t, logs.String(), s)
			}

			for _, s := range tt.notExpected {
				assert.NotContains(t, logs.String(), s)
			}
		})
	}
}
