package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	echoToolName   = "echo"
	failToolName   = "fail"
	secretToolName = "secret"
	coachAgent     = "coach"
	plainAgent     = "plain"
	validAgentsDir = "testdata/valid/agents"
	validSkillsDir = "testdata/valid/skills"
	agentsSubdir   = "agents"
	planSkill      = "plan"
	ghostSkill     = "ghost"
)

var errToolFailed = errors.New("boom")

type fakeTool struct {
	name    string
	schema  string // empty: a valid default schema
	execute func(input json.RawMessage) (string, error)
	// final makes a successful call end the run (finalTool).
	final bool
}

func (t *fakeTool) EndsRun() bool {
	return t.final
}

func (t *fakeTool) Name() string {
	return t.name
}

func (t *fakeTool) Description() string {
	return "test tool " + t.name
}

func (t *fakeTool) InputSchema() json.RawMessage {
	if t.schema != "" {
		return json.RawMessage(t.schema)
	}

	return json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}}}`)
}

func (t *fakeTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	return t.execute(input)
}

// testTools: echo returns "echo: <text>", fail always fails, secret is registered but no agent may use it.
func testTools() []Tool {
	return []Tool{
		&fakeTool{name: echoToolName, execute: func(input json.RawMessage) (string, error) {
			var in struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(input, &in); err != nil {
				return "", fmt.Errorf("failed to decode echo input: %w", err)
			}

			return "echo: " + in.Text, nil
		}},
		&fakeTool{name: failToolName, execute: func(json.RawMessage) (string, error) {
			return "", errToolFailed
		}},
		&fakeTool{name: secretToolName, execute: func(json.RawMessage) (string, error) {
			return "secret", nil
		}},
	}
}

// loadTestRegistry loads testdata/valid: agents coach and plain sharing the skill tone.
func loadTestRegistry(t *testing.T) *Registry {
	t.Helper()

	registry, err := LoadRegistry(validAgentsDir, validSkillsDir, testTools())
	require.NoError(t, err)

	return registry
}
