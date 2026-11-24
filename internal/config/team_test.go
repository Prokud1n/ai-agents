package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTeamLocation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
		fails    bool
	}{
		{name: "Moscow", input: "Europe/Moscow", expected: "Europe/Moscow"},
		{name: "Bangkok", input: "Asia/Bangkok", expected: "Asia/Bangkok"},
		{name: "empty is UTC", input: "", expected: "UTC"},
		{name: "unknown zone", input: "Mars/Olympus", fails: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			location, err := (&Team{Timezone: tt.input}).Location()
			if tt.fails {
				require.Error(t, err)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expected, location.String())
		})
	}
}
