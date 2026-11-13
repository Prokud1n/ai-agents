package internal

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/fx"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/migrator"
)

func TestDeclareOptions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		options []fx.Option
	}{
		{name: "application", options: DeclareApplicationOptions()},
		{name: "migrate up", options: DeclareMigrationOptions(migrator.UpCommand)},
		{name: "migrate down", options: DeclareMigrationOptions(migrator.DownCommand)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := fx.ValidateApp(tt.options...)

			require.NoError(t, err)
		})
	}
}

func TestNewLogger(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		level     string
		expectErr bool
	}{
		{name: "debug level", level: "debug"},
		{name: "info level", level: "info"},
		{name: "upper case level", level: "WARN"},
		{name: "error level", level: "error"},
		{name: "unknown level", level: "verbose", expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			log, err := NewLogger(config.Logging{LogLevel: tt.level})
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
