package logger

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/config"
)

func TestNewStderr(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		level     string
		expectErr bool
	}{
		{name: "info level", level: "info"},
		{name: "unknown level", level: "loud", expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			log, err := NewStderr(config.Logging{LogLevel: tt.level})
			if tt.expectErr {
				require.Error(t, err)
				assert.Nil(t, log)

				return
			}

			require.NoError(t, err)
			assert.NotNil(t, log)
		})
	}
}
