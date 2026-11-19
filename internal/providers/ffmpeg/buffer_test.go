package ffmpeg

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTailBuffer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    []string
		expected string
	}{
		{name: "empty", input: nil, expected: ""},
		{name: "fits", input: []string{"ab", "cd"}, expected: "abcd"},
		{name: "exactly full", input: []string{"abc", "def"}, expected: "abcdef"},
		{name: "wraps around", input: []string{"abcd", "efgh"}, expected: "cdefgh"},
		{name: "one write longer than the buffer", input: []string{"x", "0123456789"}, expected: "456789"},
		{name: "many small writes", input: strings.Split("abcdefghij", ""), expected: "efghij"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := newTailBuffer(6)
			for _, s := range tt.input {
				n, err := b.Write([]byte(s))
				require.NoError(t, err)
				assert.Equal(t, len(s), n)
			}

			assert.Equal(t, tt.expected, b.String())
		})
	}
}

func TestLineCollector(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    []string
		expected string
	}{
		{name: "nothing matches", input: []string{"Input #0\nStream #0:0\n"}, expected: ""},
		{
			name:     "lines split across writes",
			input:    []string{"[sd] silence_st", "art: 1\nnoise\n[sd] silence_end: 2 | d", "uration: 1\n"},
			expected: "[sd] silence_start: 1\n[sd] silence_end: 2 | duration: 1",
		},
		{name: "unterminated last line", input: []string{"x\n[sd] silence_start: 9"}, expected: "[sd] silence_start: 9"},
		{name: "too long line is dropped", input: []string{"silence_" + strings.Repeat("x", 50), "\nsilence_ok\n"}, expected: "silence_ok"},
		{name: "line limit", input: []string{"silence_1\nsilence_2\nsilence_3\nsilence_4\n"}, expected: "silence_1\nsilence_2\nsilence_3"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := &lineCollector{marker: silenceMarker, maxLines: 3, maxLine: 40}
			for _, s := range tt.input {
				n, err := c.Write([]byte(s))
				require.NoError(t, err)
				assert.Equal(t, len(s), n)
			}

			assert.Equal(t, tt.expected, c.String())
		})
	}
}
