package migrator

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseCommand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     []string
		expected  Command
		expectErr bool
	}{
		{name: "up", input: []string{string(UpCommand)}, expected: UpCommand},
		{name: "down", input: []string{string(DownCommand)}, expected: DownCommand},
		{name: "no arguments", input: nil, expectErr: true},
		{name: "unknown command", input: []string{"drop"}, expectErr: true},
		{name: "extra arguments", input: []string{string(DownCommand), "2"}, expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParseCommand(tt.input)
			if tt.expectErr {
				require.ErrorIs(t, err, errUsage)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expected, got)
		})
	}
}
