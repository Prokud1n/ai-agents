package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Prokud1n/ai-agents/internal/providers/anthropic"
)

// LoadSkillToolName is the built-in tool that returns a skill body. It is given to every agent with skills
// and must not be listed in agent.md.
const LoadSkillToolName = "load_skill"

// ToolsGroup is the fx value group of agent tools. A module adds a tool without touching the runtime:
// it provides its constructor annotated with fx.As(new(agents.Tool)) and fx.ResultTags(agents.ToolsGroup).
const ToolsGroup = `group:"agent_tools"`

// Tool is a client tool the model may call. Its error goes back to the model as a tool_result
// with is_error, so the run continues.
type Tool interface {
	Name() string
	Description() string
	// InputSchema is a JSON Schema of the input object.
	InputSchema() json.RawMessage
	Execute(ctx context.Context, input json.RawMessage) (string, error)
}

// toolAvailability is implemented by a tool that may be unavailable in this environment, e.g. without
// an optional API key. Such a tool stays registered and tells the model it is unavailable when called;
// the registry warns on start about every agent that lists it, so the tool is never lost silently.
type toolAvailability interface {
	// Available returns nil when the tool works, otherwise the reason it does not.
	Available() error
}

func toolDefinition(t Tool) anthropic.ToolDefinition {
	return anthropic.ToolDefinition{Name: t.Name(), Description: t.Description(), InputSchema: t.InputSchema()}
}

// loadSkillTool returns skill bodies of one agent: progressive disclosure keeps them out of the system prompt.
type loadSkillTool struct {
	agent *Agent
}

type loadSkillInput struct {
	Name string `json:"name"`
}

func (*loadSkillTool) Name() string {
	return LoadSkillToolName
}

func (*loadSkillTool) Description() string {
	return "Loads the full instructions of one of your skills by name. " +
		"Call it before doing a task that matches a skill description, then follow the instructions."
}

func (t *loadSkillTool) InputSchema() json.RawMessage {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{
				"type":        "string",
				"description": "Skill name from the list in the system prompt.",
				"enum":        t.agent.skillNames(),
			},
		},
		"required":             []string{"name"},
		"additionalProperties": false,
	}

	data, err := json.Marshal(schema)
	if err != nil {
		// A map of strings and slices always encodes; keep the tool usable anyway.
		return json.RawMessage(`{"type":"object"}`)
	}

	return data
}

func (t *loadSkillTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var in loadSkillInput
	if err := json.Unmarshal(input, &in); err != nil {
		return "", NewToolError(`input must be {"name": "<skill name>"}`)
	}

	skill, ok := t.agent.skill(in.Name)
	if !ok {
		return "", NewToolError(fmt.Sprintf("unknown skill %q, available skills: %s",
			in.Name, strings.Join(t.agent.skillNames(), ", ")))
	}

	return skill.instructions(), nil
}
