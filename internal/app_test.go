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

// TestApplicationWiresGatewayToTasks checks only that the application graph resolves gateway.Handler,
// tasks.Notifier and teamchat.Handler: ValidateApp runs no constructors. That the gateway really gets
// teamchat.Handler and drafts go to Telegram is checked by teamchat's TestIntegrationGatewayTaskCycle; the
// notifier choice by the config — by TestNewNotifierFollowsGateway.
func TestApplicationWiresGatewayToTasks(t *testing.T) {
	t.Parallel()

	err := fx.ValidateApp(append(DeclareApplicationOptions(),
		fx.Invoke(func(gateway.Handler, tasks.Notifier, *teamchat.Handler) {}),
	)...)

	require.NoError(t, err)
}
