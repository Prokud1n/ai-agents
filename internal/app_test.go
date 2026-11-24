package internal

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/fx"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/editor"
	"github.com/Prokud1n/ai-agents/internal/editplan"
	"github.com/Prokud1n/ai-agents/internal/gateway"
	"github.com/Prokud1n/ai-agents/internal/media"
	"github.com/Prokud1n/ai-agents/internal/mediaworker"
	"github.com/Prokud1n/ai-agents/internal/providers/anthropic"
	"github.com/Prokud1n/ai-agents/internal/providers/postgres"
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
		{name: "media worker", options: DeclareMediaWorkerOptions()},
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
// tasks.Notifier, teamchat.Handler and the media store: ValidateApp runs no constructors. That the gateway really gets
// teamchat.Handler and drafts go to Telegram is checked by teamchat's TestIntegrationGatewayTaskCycle; the
// notifier choice by the config — by TestNewNotifierFollowsGateway.
func TestApplicationWiresGatewayToTasks(t *testing.T) {
	t.Parallel()

	err := fx.ValidateApp(append(DeclareApplicationOptions(),
		fx.Invoke(func(gateway.Handler, tasks.Notifier, *teamchat.Handler) {}),
		fx.Invoke(func(*media.Store) {}),
		fx.Invoke(func(tasks.Executor, *editor.Executor, *editplan.Decider) {}),
	)...)

	require.NoError(t, err)
}

// TestMediaWorkerHasNoAppSecrets: the media worker's graph builds the worker without the Telegram gateway, the
// Claude API client, the editor's model (editplan) or PostgreSQL (ffmpeg there parses files from users).
func TestMediaWorkerHasNoAppSecrets(t *testing.T) {
	t.Parallel()

	require.NoError(t, fx.ValidateApp(append(DeclareMediaWorkerOptions(),
		fx.Invoke(func(*mediaworker.Worker) {}))...))

	for _, invoke := range []any{
		func(*gateway.Sender) {},
		func(*anthropic.Client) {},
		func(*editplan.Decider) {},
		func(*postgres.Client) {},
		func(*config.Telegram) {},
		func(*config.Anthropic) {},
	} {
		require.Error(t, fx.ValidateApp(append(DeclareMediaWorkerOptions(), fx.Invoke(invoke))...))
	}
}
