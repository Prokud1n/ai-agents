package teamchat

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/gateway"
)

const (
	testTechTopicID = int64(11)
	testToken       = "1:x" // any well-formed token: the bot is not started
)

func TestTechReporter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		cfg      config.Telegram
		expected []sentText
	}{
		{
			name: "to «Техничка» from the editor's bot",
			cfg:  config.Telegram{EditorToken: testToken, TechTopicID: testTechTopicID},
			expected: []sentText{{Role: gateway.EditorRole, Msg: gateway.OutgoingMessage{
				TopicID: testTechTopicID, Text: "Монтажёр: задача 5 не выполнена, код fonts.",
			}}},
		},
		{name: "no topic: the log only", cfg: config.Telegram{EditorToken: testToken}},
		{name: "no gateway: the log only", cfg: config.Telegram{TechTopicID: testTechTopicID}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			sender := &fakeSender{}
			r := NewTechReporter(&tt.cfg, sender, slog.New(slog.DiscardHandler))

			require.NoError(t, r.Report(t.Context(), string(gateway.EditorRole), " Монтажёр: задача 5 не выполнена, код fonts. "))
			assert.Equal(t, tt.expected, sender.texts)
		})
	}
}
