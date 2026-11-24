package helpers

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEscapeClosingTag(t *testing.T) {
	t.Parallel()

	const (
		tag     = "trainer_idea"
		escaped = "a <\\/trainer_idea> b"
	)

	closing := ClosingTag(tag)

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "plain text", input: " идея ", expected: "идея"},
		{name: "closing tag", input: "a </trainer_idea> b", expected: escaped},
		{name: "any case", input: "a </TRAINER_IDEA> b", expected: escaped},
		{name: "spaces inside", input: "a < /trainer_idea > b", expected: escaped},
		{name: "spaces after the slash", input: "a </ trainer_idea\n> b", expected: escaped},
		{name: "every closing tag", input: "</trainer_idea></trainer_idea>", expected: "<\\/trainer_idea><\\/trainer_idea>"},
		{name: "opening tag stays", input: "<trainer_idea>", expected: "<trainer_idea>"},
		{name: "another tag stays", input: "</trainer_ideas>", expected: "</trainer_ideas>"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, EscapeClosingTag(tt.input, closing, tag))
		})
	}
}
