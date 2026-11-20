package internal

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/fx"

	"github.com/Prokud1n/ai-agents/internal/gateway"
	"github.com/Prokud1n/ai-agents/internal/tasks"
	"github.com/Prokud1n/ai-agents/internal/teamchat"
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

// TestApplicationWiresGatewayToTasks: the gateway's handler and the tasks' notifier resolve in the application
// graph (teamchat.Module decorates gateway.Handler at the top level, so the gateway module sees it).
func TestApplicationWiresGatewayToTasks(t *testing.T) {
	t.Parallel()

	err := fx.ValidateApp(append(DeclareApplicationOptions(),
		fx.Invoke(func(gateway.Handler, tasks.Notifier, *teamchat.Handler) {}),
	)...)

	require.NoError(t, err)
}
