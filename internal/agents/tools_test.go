package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

const (
	echoToolName   = "echo"
	failToolName   = "fail"
	secretToolName = "secret"
	coachAgent     = "coach"
	plainAgent     = "plain"
	validAgentsDir = "testdata/valid"
)

var errToolFailed = errors.New("boom")

type fakeTool struct {
	name    string
	execute func(input json.RawMessage) (string, error)
}

func (t *fakeTool) Name() string {
	return t.name
}

func (t *fakeTool) Description() string {
	return "test tool " + t.name
}

func (*fakeTool) InputSchema() json.RawMessage {
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
