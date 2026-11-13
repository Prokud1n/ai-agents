package agents

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadRegistryValid(t *testing.T) {
	t.Parallel()

	registry, err := LoadRegistry(validAgentsDir, testTools())
	require.NoError(t, err)

	assert.Equal(t, []string{coachAgent, plainAgent}, registry.Names())

	coach, err := registry.Get(coachAgent)
	require.NoError(t, err)

	expected := &Agent{
		Name:        coachAgent,
		DisplayName: "Тренер",
		Tools:       []string{echoToolName, failToolName},
		Skills: []Skill{
			{Name: "plan", Description: "Plans a week of workouts.", Body: "PLAN BODY: three workouts a week."},
			{Name: "tone", Description: "Keeps the brand tone of voice.", Body: "TONE BODY: friendly, no jargon."},
		},
		Prompt: "You are a test coach.",
	}
	assert.Equal(t, expected, coach)

	plain, err := registry.Get(plainAgent)
	require.NoError(t, err)
	assert.Equal(t, "claude-test", plain.Model)
	assert.Equal(t, "You are a plain agent.", plain.Prompt)
	assert.Empty(t, plain.Skills)

	_, err = registry.Get("ghost")
	require.ErrorIs(t, err, ErrAgentNotFound)
}

func TestLoadRegistryInvalid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		expected string
	}{
		{name: "missing_name", expected: "name is required"},
		{name: "name_mismatch", expected: `name "other" must match the directory name "broken"`},
		{name: "missing_display_name", expected: "display_name is required"},
		{name: "empty_body", expected: "body (the system prompt of the role) is empty"},
		{name: "unknown_tool", expected: `unknown tool "telegram_send", registered tools: [echo fail secret]`},
		{name: "builtin_tool", expected: `tool "load_skill" is built in`},
		{name: "missing_skill", expected: `skill "ghost" (skills/ghost/SKILL.md): skill is listed in agent.md but its file is missing`},
		{name: "duplicate_skill", expected: `skills: "plan" is listed twice`},
		{name: "unknown_field", expected: "field skils not found"},
		{name: "no_frontmatter", expected: "file must start with a \"---\" line"},
		{name: "unclosed_frontmatter", expected: "frontmatter is not closed"},
		{name: "skill_without_description", expected: "description is required"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := filepath.Join("testdata", "invalid", tt.name)

			registry, err := LoadRegistry(dir, testTools())

			require.ErrorIs(t, err, ErrInvalidAgent)
			assert.Nil(t, registry)
			assert.Contains(t, err.Error(), filepath.Join(dir, "broken", agentFileName))
			assert.Contains(t, err.Error(), tt.expected)
		})
	}
}

func TestLoadRegistryErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		dir   string
		tools []Tool
	}{
		{name: "missing directory", dir: "testdata/missing", tools: testTools()},
		{
			name:  "duplicate tool",
			dir:   validAgentsDir,
			tools: append(testTools(), &fakeTool{name: echoToolName}),
		},
		{name: "tool named load_skill", dir: validAgentsDir, tools: []Tool{&fakeTool{name: LoadSkillToolName}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			registry, err := LoadRegistry(tt.dir, tt.tools)

			require.Error(t, err)
			assert.Nil(t, registry)
		})
	}
}

// TestExampleAgent keeps the template in agents/_example valid: it is shipped in the image and loaded on start.
func TestExampleAgent(t *testing.T) {
	t.Parallel()

	registry, err := LoadRegistry(filepath.Join("..", "..", "agents"), nil) // the real AGENTS_DIR
	require.NoError(t, err)

	example, err := registry.Get("_example")
	require.NoError(t, err)
	assert.NotEmpty(t, example.Prompt)
	require.Len(t, example.Skills, 1)
	assert.Equal(t, "greeting", example.Skills[0].Name)
}
