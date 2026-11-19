package agents

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	exampleAgent  = "_example"
	toneSkill     = "tone"
	toneBody      = "TONE BODY: friendly, no jargon."
	coachOverride = "Apply the tone to workout plans too."

	descriptionRequired = "description is required"
)

func invalidCaseDir(name string) string {
	return filepath.Join("testdata", "invalid", name)
}

func TestLoadRegistryValid(t *testing.T) {
	t.Parallel()

	registry := loadTestRegistry(t)

	// Hidden and "_" directories are skipped: .hidden, _template (agents) and _draft (skills).
	assert.Equal(t, []string{coachAgent, plainAgent}, registry.Names())
	assert.Equal(t, []string{toneSkill, "unused"}, registry.sharedSkills)

	coach, err := registry.Get(coachAgent)
	require.NoError(t, err)

	expected := &Agent{
		Name:        coachAgent,
		DisplayName: "Тренер",
		Tools:       []string{echoToolName, failToolName},
		Skills: []Skill{
			{Name: planSkill, Description: "Plans a week of workouts.", Body: "PLAN BODY: three workouts a week."},
			{Name: toneSkill, Description: "Keeps the brand tone of voice.", Body: toneBody, Override: coachOverride},
		},
		Prompt: "You are a test coach.",
	}
	assert.Equal(t, expected, coach)

	// The shared skill is used by two agents; the override belongs to coach only.
	plain, err := registry.Get(plainAgent)
	require.NoError(t, err)
	assert.Equal(t, plainModel, plain.Model)
	assert.Equal(t, "You are a plain agent.", plain.Prompt)
	assert.Equal(t, []Skill{{Name: toneSkill, Description: "Keeps the brand tone of voice.", Body: toneBody}},
		plain.Skills)

	_, err = registry.Get(ghostSkill)
	require.ErrorIs(t, err, ErrAgentNotFound)
}

func TestLoadRegistryInvalid(t *testing.T) {
	t.Parallel()

	const agentFile = "agents/broken/agent.md"

	tests := []struct {
		name     string
		file     string // the file the error points to, relative to the case directory
		expected string
	}{
		{name: "missing_name", file: agentFile, expected: "name is required"},
		{name: "name_mismatch", file: agentFile, expected: `name "other" must match the directory name "broken"`},
		{name: "missing_display_name", file: agentFile, expected: "display_name is required"},
		{name: "empty_body", file: agentFile, expected: "body (the system prompt of the role) is empty"},
		{
			name: "unknown_tool", file: agentFile,
			expected: `unknown tool "telegram_send", registered tools: [echo fail secret]`,
		},
		{name: "builtin_tool", file: agentFile, expected: `tool "load_skill" is built in`},
		{
			name: "missing_skill", file: agentFile,
			expected: `skill "ghost": not found: expected a local skills/ghost/SKILL.md or a shared ` +
				filepath.Join(invalidCaseDir("missing_skill"), skillsDirName, ghostSkill, skillFileName),
		},
		{name: "duplicate_skill", file: agentFile, expected: `skills: "plan" is listed twice`},
		{name: "unknown_field", file: agentFile, expected: "field skils not found"},
		{name: "no_frontmatter", file: agentFile, expected: "file must start with a \"---\" line"},
		{name: "unclosed_frontmatter", file: agentFile, expected: "frontmatter is not closed"},
		{name: "skill_without_description", file: agentFile, expected: descriptionRequired},
		{
			name: "override_unlisted", file: agentFile,
			expected: `skill_overrides: skill "tone" is not in skills [plan]`,
		},
		{name: "empty_override", file: agentFile, expected: `skill_overrides: override of "plan" is empty`},
		{
			name: "skill_conflict", file: agentFile,
			expected: `local skill "plan" (skills/plan/SKILL.md) has the name of a shared skill (` +
				filepath.Join(invalidCaseDir("skill_conflict"), skillsDirName, planSkill, skillFileName) + ")",
		},
		{name: "shared_skill_invalid", file: "skills/tone/SKILL.md", expected: descriptionRequired},
		{
			name: "template_skill", file: agentFile,
			expected: `skill "_draft": skill directories starting with "_" are templates and are skipped: rename the skill`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := invalidCaseDir(tt.name)

			registry, err := LoadRegistry(filepath.Join(dir, agentsSubdir), filepath.Join(dir, skillsDirName), testTools())

			require.ErrorIs(t, err, ErrInvalidAgent)
			assert.Nil(t, registry)
			assert.Contains(t, err.Error(), filepath.Join(dir, tt.file))
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
		{name: "missing agents directory", dir: "testdata/missing", tools: testTools()},
		{
			name:  "duplicate tool",
			dir:   validAgentsDir,
			tools: append(testTools(), &fakeTool{name: echoToolName}),
		},
		{name: "tool named load_skill", dir: validAgentsDir, tools: []Tool{&fakeTool{name: LoadSkillToolName}}},
		{name: "tool name with a dot", dir: validAgentsDir, tools: []Tool{&fakeTool{name: "kb.search"}}},
		{name: "empty tool name", dir: validAgentsDir, tools: []Tool{&fakeTool{name: ""}}},
		{name: "tool name over 64 chars", dir: validAgentsDir, tools: []Tool{&fakeTool{name: strings.Repeat("a", 65)}}},
		{name: "invalid input schema", dir: validAgentsDir, tools: []Tool{&fakeTool{name: "kb", schema: `{"type":`}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			registry, err := LoadRegistry(tt.dir, validSkillsDir, tt.tools)

			require.Error(t, err)
			assert.Nil(t, registry)
		})
	}
}

// A missing SKILLS_DIR means no shared skills: agents that do not list any still load, the others fail
// with the expected path.
func TestLoadRegistryWithoutSharedSkills(t *testing.T) {
	t.Parallel()

	const missingSkillsDir = "testdata/no-such-skills"

	registry, err := LoadRegistry(validAgentsDir, missingSkillsDir, testTools())
	require.ErrorIs(t, err, ErrInvalidAgent, "coach and plain list the shared skill tone")
	assert.Nil(t, registry)
	assert.Contains(t, err.Error(), `skill "tone": not found`)

	registry, err = LoadRegistry(filepath.Join("testdata", "noskills", agentsSubdir), missingSkillsDir, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"solo"}, registry.Names())
	assert.Empty(t, registry.sharedSkills)
}

func TestRegisterToolsValidNames(t *testing.T) {
	t.Parallel()

	tools := []Tool{&fakeTool{name: "Search_KB-2"}, &fakeTool{name: strings.Repeat("a", 64)}}

	byName, err := registerTools(tools)

	require.NoError(t, err)
	assert.Len(t, byName, 2)
}

// TestExampleAgent keeps the template in agents/_example valid with the real shared skills. Templates
// ("_" prefix) are not loaded as agents, so the template is loaded directly, without registered tools.
func TestExampleAgent(t *testing.T) {
	t.Parallel()

	repoRoot := filepath.Join("..", "..")
	agentsDir := filepath.Join(repoRoot, agentsSubdir)  // the real AGENTS_DIR
	skillsDir := filepath.Join(repoRoot, skillsDirName) // the real SKILLS_DIR

	shared, err := loadSharedSkills(os.DirFS(skillsDir), skillsDir)
	require.NoError(t, err)

	example, err := loadAgent(os.DirFS(agentsDir), exampleAgent, shared, nil)
	require.NoError(t, err)
	assert.Equal(t, exampleAgent, example.Name)
	assert.NotEmpty(t, example.Prompt)
	require.NotEmpty(t, example.Skills)

	overridden := 0

	for _, skill := range example.Skills {
		if skill.Override != "" {
			overridden++
		}
	}

	assert.Equal(t, 1, overridden, "the template shows a shared skill with an override")

	registry, err := LoadRegistry(agentsDir, skillsDir, nil)
	require.NoError(t, err)
	assert.NotContains(t, registry.Names(), exampleAgent, "templates are not loaded as agents")
}
