package gateway

import (
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/providers/telegram"
)

// testMenu is the menu of every gateway started by the tests.
var testMenu = Menu{Commands: []MenuCommand{
	{Command: HelpCommand, Description: "Что умеет команда"},
	{Command: StatusCommand, Description: "Задачи"},
}}

// TestGatewayMenu: the marketer gets the menu in the group, the other bots lose theirs in every scope that the group
// could fall back to.
func TestGatewayMenu(t *testing.T) {
	t.Parallel()

	api, baseURL := newFakeBotAPI(t)
	lc, _ := startGateway(t, testConfig(baseURL, t.TempDir()), newMemoryStore(), newRecordingHandler(),
		slog.New(slog.DiscardHandler))

	require.Eventually(t, func() bool {
		return len(api.callsOf("setMyCommands")) == 1 && len(api.callsOf("deleteMyCommands")) == 3*(len(Roles)-1)
	}, eventTimeout, 10*time.Millisecond)
	lc.RequireStop()

	set := api.callsOf("setMyCommands")[0]
	assert.Equal(t, marketerAuth, set.token, "only the marketer shows the menu")

	var params telegram.SetMyCommandsParams
	require.NoError(t, json.Unmarshal(set.body, &params))
	assert.Equal(t, []telegram.BotCommand{
		{Command: "help", Description: "Что умеет команда"},
		{Command: "status", Description: "Задачи"},
	}, params.Commands)
	assert.Equal(t, &telegram.BotCommandScope{Type: telegram.ChatCommandScope, ChatID: testGroupID}, params.Scope)

	deleted := map[string][]string{}

	for _, call := range api.callsOf("deleteMyCommands") {
		var del telegram.DeleteMyCommandsParams
		require.NoError(t, json.Unmarshal(call.body, &del))
		deleted[call.token] = append(deleted[call.token], del.Scope.Type)
	}

	assert.NotContains(t, deleted, marketerAuth)

	for _, token := range []string{copywriterAuth, editorAuth, designerAuth} {
		assert.ElementsMatch(t, []string{
			telegram.ChatCommandScope, telegram.AllGroupChatsCommandScope, telegram.DefaultCommandScope,
		}, deleted[token])
	}
}

func TestMenuScopes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    int64
		expected []*telegram.BotCommandScope
	}{
		{
			name:  "group, all groups, default",
			input: testGroupID,
			expected: []*telegram.BotCommandScope{
				{Type: telegram.ChatCommandScope, ChatID: testGroupID},
				{Type: telegram.AllGroupChatsCommandScope},
				{Type: telegram.DefaultCommandScope},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, menuScopes(tt.input))
		})
	}
}
