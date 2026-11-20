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

			registry, err := NewRegistry(&config.Agents{Dir: tt.dir, SkillsDir: validSkillsDir}, &config.LLM{DefaultModel: testModel},
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

// offlineTool is a tool that reports itself unavailable, like the knowledge base without its API key.
type offlineTool struct {
	fakeTool
}

func (*offlineTool) Available() error {
	return errToolFailed
}

func TestNewRegistryUnavailableTool(t *testing.T) {
	t.Parallel()

	const unavailableWarning = "tool is unavailable"

	tests := []struct {
		name        string
		tool        Tool
		expected    []string
		notExpected []string
	}{
		{
			name: "unavailable tool listed by an agent",
			tool: &offlineTool{fakeTool: fakeTool{name: failToolName}},
			// coach lists fail; plain lists no tools.
			expected:    []string{unavailableWarning, `"agent":"coach"`, `"tool":"fail"`, `"reason":"boom"`},
			notExpected: []string{`"agent":"plain"`},
		},
		{
			name:        "unavailable tool no agent lists",
			tool:        &offlineTool{fakeTool: fakeTool{name: secretToolName}},
			notExpected: []string{unavailableWarning},
		},
		{
			name:        "available tool",
			tool:        &fakeTool{name: failToolName},
			notExpected: []string{unavailableWarning},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tools := make([]Tool, 0, len(testTools()))
			for _, tool := range testTools() {
				if tool.Name() != tt.tool.Name() {
					tools = append(tools, tool)
				}
			}

			tools = append(tools, tt.tool)

			var logs bytes.Buffer

			log := slog.New(slog.NewJSONHandler(&logs, nil))

			_, err := NewRegistry(&config.Agents{Dir: validAgentsDir, SkillsDir: validSkillsDir},
				&config.LLM{DefaultModel: testModel}, fakePrices{plainModel: true, testModel: true}, log, tools)
			require.NoError(t, err)

			for _, s := range tt.expected {
				assert.Contains(t, logs.String(), s)
			}

			for _, s := range tt.notExpected {
				assert.NotContains(t, logs.String(), s)
			}
		})
	}
}
