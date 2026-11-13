package health

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type pingerFunc func(ctx context.Context) error

func (f pingerFunc) Ping(ctx context.Context) error {
	return f(ctx)
}

func TestNewChecker(t *testing.T) {
	t.Parallel()

	errPing := errors.New("ping failed")

	tests := []struct {
		name        string
		inputName   string
		inputPinger Pinger
		expected    error
	}{
		{name: "healthy", inputName: postgresName, inputPinger: pingerFunc(func(context.Context) error { return nil })},
		{
			name:        "unhealthy",
			inputName:   redisName,
			inputPinger: pingerFunc(func(context.Context) error { return errPing }),
			expected:    errPing,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			checker := NewChecker(tt.inputName, tt.inputPinger)

			assert.Equal(t, tt.inputName, checker.Name())
			require.ErrorIs(t, checker.Ping(t.Context()), tt.expected)
		})
	}
}
