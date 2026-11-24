package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/providers/telegram"
)

func TestSenderPinMessage(t *testing.T) {
	t.Parallel()

	sender, api := newTestSender(t)

	require.NoError(t, sender.PinMessage(t.Context(), MarketerRole, 77))
	require.NoError(t, sender.UnpinMessage(t.Context(), MarketerRole, 76))

	pins := api.callsOf("pinChatMessage")
	require.Len(t, pins, 1)
	assert.Equal(t, marketerAuth, pins[0].token)

	var pin telegram.PinChatMessageParams
	require.NoError(t, json.Unmarshal(pins[0].body, &pin))
	assert.Equal(t, telegram.PinChatMessageParams{ChatID: testGroupID, MessageID: 77, DisableNotification: true}, pin)

	unpins := api.callsOf("unpinChatMessage")
	require.Len(t, unpins, 1)

	var unpin telegram.UnpinChatMessageParams
	require.NoError(t, json.Unmarshal(unpins[0].body, &unpin))
	assert.Equal(t, telegram.UnpinChatMessageParams{ChatID: testGroupID, MessageID: 76}, unpin)
}

// TestSenderErrorClasses: the errors of pins and edits the team chat tells apart.
func TestSenderErrorClasses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		code        int
		description string
		call        func(s *Sender) error
		expected    error
	}{
		{
			name: "pin without the right", code: http.StatusBadRequest,
			description: "Bad Request: not enough rights to manage pinned messages in the chat",
			call:        func(s *Sender) error { return s.PinMessage(t.Context(), EditorRole, 1) },
			expected:    ErrNoRights,
		},
		{
			name: "pin by a bot that is not an admin", code: http.StatusBadRequest,
			description: "Bad Request: CHAT_ADMIN_REQUIRED",
			call:        func(s *Sender) error { return s.UnpinMessage(t.Context(), EditorRole, 1) },
			expected:    ErrNoRights,
		},
		{
			name: "pin by a bot out of the group", code: http.StatusForbidden,
			description: "Forbidden: bot is not a member of the supergroup chat",
			call:        func(s *Sender) error { return s.PinMessage(t.Context(), EditorRole, 1) },
			expected:    ErrNoRights,
		},
		{
			name: "edit to the same text", code: http.StatusBadRequest,
			description: "Bad Request: message is not modified: specified new message content and reply markup are " +
				"exactly the same as a current content and reply markup of the message",
			call:     func(s *Sender) error { return s.EditText(t.Context(), EditorRole, 1, "текст", nil) },
			expected: ErrNotModified,
		},
		{
			name: "edit of a deleted message", code: http.StatusBadRequest,
			description: "Bad Request: message to edit not found",
			call:        func(s *Sender) error { return s.EditText(t.Context(), EditorRole, 1, "текст", nil) },
			expected:    ErrMessageNotFound,
		},
		{
			name: "pin of a pinned message is done", code: http.StatusBadRequest,
			description: "Bad Request: CHAT_NOT_MODIFIED",
			call:        func(s *Sender) error { return s.PinMessage(t.Context(), EditorRole, 1) },
			expected:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/getMe") {
					writeResult(w, telegram.User{ID: 3, IsBot: true})

					return
				}

				writeError(w, tt.code, tt.description)
			}))
			t.Cleanup(srv.Close)

			cfg := testConfig(srv.URL, t.TempDir())
			err := tt.call(NewSender(testBots(t, cfg), cfg))

			if tt.expected == nil {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, tt.expected)
		})
	}
}
