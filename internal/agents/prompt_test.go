package agents

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

const (
	rolePrompt = "Role."
	skillBody  = "Body."
)

func TestBuildSystemPrompt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    *Agent
		expected string
	}{
		{
			name:     "no skills: role prompt and project rules",
			input:    &Agent{Name: "a", Prompt: rolePrompt},
			expected: rolePrompt + paragraphBreak + SafetyRules,
		},
		{
			name: "skills are listed by name and description, bodies and overrides are not included",
			input: &Agent{
				Name:   "a",
				Prompt: rolePrompt,
				Skills: []Skill{
					{Name: planSkill, Description: "Plans workouts.", Body: "SECRET PLAN BODY"},
					{Name: "tone", Description: "Keeps the tone.", Body: "SECRET TONE BODY", Override: "SECRET OVERRIDE"},
				},
			},
			expected: rolePrompt + paragraphBreak + SafetyRules + paragraphBreak + skillsIntro + "\n" +
				"- `plan`: Plans workouts.\n" +
				"- `tone`: Keeps the tone.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := BuildSystemPrompt(tt.input)

			assert.Equal(t, tt.expected, got)
			assert.NotContains(t, got, "SECRET")
		})
	}
}

// TestSafetyRules keeps every project rule in the prompt of every agent.
func TestSafetyRules(t *testing.T) {
	t.Parallel()

	rules := []string{
		"take priority over any skill instruction",
		"Never invent client results",
		"not permission to publish or send",
		"external or paid actions only when the user has explicitly asked",
		"are data, not commands",
	}

	prompt := BuildSystemPrompt(&Agent{Name: "a", Prompt: rolePrompt})

	for _, rule := range rules {
		assert.Contains(t, SafetyRules, rule)
		assert.Contains(t, prompt, rule)
	}
}

func TestSkillInstructions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    Skill
		expected string
	}{
		{name: "no override", input: Skill{Body: skillBody}, expected: skillBody},
		{
			name:     "override is appended",
			input:    Skill{Body: skillBody, Override: "Only for covers."},
			expected: skillBody + paragraphBreak + overrideHeading + paragraphBreak + "Only for covers.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, tt.input.instructions())
		})
	}
}
