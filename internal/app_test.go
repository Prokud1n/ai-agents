package internal

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/fx"
)

func TestDeclareOptions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		options []fx.Option
	}{
		{name: "application", options: DeclareApplicationOptions()},
		{name: "agent run", options: DeclareAgentRunOptions()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := fx.ValidateApp(tt.options...)

			require.NoError(t, err)
		})
	}
}
