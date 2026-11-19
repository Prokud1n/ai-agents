package agents

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const nameOnlyFrontmatter = "name: a\n"

func TestSplitFrontmatter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		input               string
		expectedFrontmatter string
		expectedBody        string
		expectErr           bool
	}{
		{
			name:                "frontmatter and body",
			input:               "---\nname: a\n---\n\nBody line.\n",
			expectedFrontmatter: nameOnlyFrontmatter,
			expectedBody:        "Body line.",
		},
		{
			name:                "windows line endings and bom",
			input:               "\ufeff---\r\nname: a\r\n---\r\nBody\r\n",
			expectedFrontmatter: nameOnlyFrontmatter,
			expectedBody:        "Body",
		},
		{
			name:                "dashes inside the body are kept",
			input:               "---\nname: a\n---\nBody\n---\nMore",
			expectedFrontmatter: nameOnlyFrontmatter,
			expectedBody:        "Body\n---\nMore",
		},
		{
			name:                "empty frontmatter and body",
			input:               "---\n---\n",
			expectedFrontmatter: "",
			expectedBody:        "",
		},
		{name: "no frontmatter", input: "Body only", expectErr: true},
		{name: "not closed", input: "---\nname: a\nBody", expectErr: true},
		{name: "empty file", input: "", expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			doc, err := splitFrontmatter([]byte(tt.input))
			if tt.expectErr {
				require.ErrorIs(t, err, ErrInvalidAgent)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expectedFrontmatter, string(doc.frontmatter))
			assert.Equal(t, tt.expectedBody, doc.body)
		})
	}
}

func TestDecodeFrontmatter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     string
		expected  agentFrontmatter
		expectErr bool
	}{
		{
			name:  "all fields",
			input: "name: a\ndisplay_name: A\nmodel: m\ntools: [x, y]\nskills:\n  - s\n",
			expected: agentFrontmatter{
				Name: "a", DisplayName: "A", Model: "m", Tools: []string{"x", "y"}, Skills: []string{"s"},
			},
		},
		{name: "empty", input: "", expected: agentFrontmatter{}},
		{name: "unknown field", input: "name: a\nskils: [s]\n", expectErr: true},
		{name: "invalid yaml", input: "name: [a\n", expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var got agentFrontmatter

			err := decodeFrontmatter([]byte(tt.input), &got)
			if tt.expectErr {
				require.ErrorIs(t, err, ErrInvalidAgent)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expected, got)
		})
	}
}
