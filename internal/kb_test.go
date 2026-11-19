package internal

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/fx"

	"github.com/Prokud1n/ai-agents/internal/kbcli"
)

func TestDeclareKBOptions(t *testing.T) {
	t.Parallel()

	var runner *kbcli.Runner

	err := fx.ValidateApp(append(DeclareKBOptions(), fx.Populate(&runner))...)

	require.NoError(t, err)
}

func TestRunKBUsage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
	}{
		{name: "no command", args: nil},
		{name: "unknown command", args: []string{"list"}},
		{name: "missing argument", args: []string{"search"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, KBExitUsage, RunKB(tt.args))
		})
	}
}
