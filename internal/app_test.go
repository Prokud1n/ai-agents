package internal

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/editor"
	"github.com/Prokud1n/ai-agents/internal/editplan"
	"github.com/Prokud1n/ai-agents/internal/gateway"
	"github.com/Prokud1n/ai-agents/internal/ideas"
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
		fx.Invoke(func(*ideas.Service) {}),
		fx.Invoke(fx.Annotate(func([]tasks.Route) {}, fx.ParamTags(tasks.RoutesGroup))),
	)...)

	require.NoError(t, err)
}

// TestTaskRoutes builds the routes the editor and the ideas modules put into tasks.RoutesGroup (editor.Route and
// ideas.Route are parts of their modules): without the group tag their tasks would silently go to the agents' model.
func TestTaskRoutes(t *testing.T) {
	t.Parallel()

	editorExecutor, ideaExecutor := &editor.Executor{}, &ideas.Executor{}

	var routes []tasks.Route

	app := fxtest.New(t,
		editor.Route,
		ideas.Route,
		fx.Supply(editorExecutor, ideaExecutor),
		fx.Invoke(fx.Annotate(func(group []tasks.Route) { routes = group }, fx.ParamTags(tasks.RoutesGroup))),
	)
	app.RequireStart()
	app.RequireStop()

	byKey := make(map[string]tasks.Executor, len(routes))
	for _, r := range routes {
		byKey[r.Agent+"/"+r.Kind] = r.Executor
	}

	assert.Equal(t, map[string]tasks.Executor{
		editor.AgentName + "/":                 editorExecutor,
		ideas.AgentName + "/" + ideas.TaskKind: ideaExecutor,
	}, byKey)

	_, err := tasks.NewRoutingExecutor(nil, routes)
	require.NoError(t, err, "the routes do not overlap")
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
