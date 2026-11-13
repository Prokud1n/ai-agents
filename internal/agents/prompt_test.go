package agents

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

const rolePrompt = "Role."

func TestBuildSystemPrompt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    *Agent
		expected string
	}{
		{
			name:     "no skills: role prompt only",
			input:    &Agent{Name: "a", Prompt: rolePrompt},
			expected: rolePrompt,
		},
		{
			name: "skills are listed by name and description, bodies are not included",
			input: &Agent{
				Name:   "a",
				Prompt: rolePrompt,
				Skills: []Skill{
					{Name: "plan", Description: "Plans workouts.", Body: "SECRET PLAN BODY"},
					{Name: "tone", Description: "Keeps the tone.", Body: "SECRET TONE BODY"},
				},
			},
			expected: rolePrompt + "\n\n" + skillsIntro + "\n" +
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
