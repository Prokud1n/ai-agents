package teamchat

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/gateway"
	"github.com/Prokud1n/ai-agents/internal/providers/redis"
	"github.com/Prokud1n/ai-agents/internal/providers/telegram"
)

const supergroupType = "supergroup"

type botCall struct {
	token  string
	method string
	body   []byte
	result int64 // the ID of a sent message
}

// fakeBotAPI is a Bot API server on httptest: getMe answers from users (by token), getUpdates hands out
// the queued updates once, sends get new message IDs, every call except polling is recorded.
type fakeBotAPI struct {
	mu      sync.Mutex
	groupID int64
	users   map[string]telegram.User
	updates map[string][]telegram.Update
	calls   []botCall
	nextID  int64
	baseURL string
}

func newFakeBotAPI(t *testing.T, groupID int64, users map[string]telegram.User) *fakeBotAPI {
	t.Helper()

	api := &fakeBotAPI{
		groupID: groupID,
		users:   users,
		updates: map[string][]telegram.Update{},
		nextID:  5000,
	}

	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)

	api.baseURL = srv.URL

	return api
}

func (f *fakeBotAPI) queue(token string, updates ...telegram.Update) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.updates[token] = append(f.updates[token], updates...)
}

func (f *fakeBotAPI) callsOf(method string) []botCall {
	f.mu.Lock()
	defer f.mu.Unlock()

	var calls []botCall

	for _, c := range f.calls {
		if c.method == method {
			calls = append(calls, c)
		}
	}

	return calls
}

func (f *fakeBotAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	token, method, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/bot"), "/")
	body, _ := io.ReadAll(r.Body)

	f.mu.Lock()
	user, known := f.users[token]
	f.mu.Unlock()

	if !known {
		writeAPI(w, http.StatusUnauthorized, map[string]any{"ok": false, "error_code": 401, "description": "Unauthorized"})

		return
	}

	switch method {
	case "getMe":
		writeResult(w, user)
	case "getUpdates":
		writeResult(w, f.takeUpdates(r.Context(), token))
	case "answerCallbackQuery", "setMessageReaction":
		f.record(token, method, body, 0)
		writeResult(w, true)
	default: // sendMessage, edits
		f.mu.Lock()
		f.nextID++
		id := f.nextID
		f.mu.Unlock()

		f.record(token, method, body, id)
		writeResult(w, telegram.Message{MessageID: id, Chat: telegram.Chat{ID: f.groupID, Type: supergroupType}})
	}
}

func (f *fakeBotAPI) record(token, method string, body []byte, result int64) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls = append(f.calls, botCall{token: token, method: method, body: body, result: result})
}

// takeUpdates returns the queued updates; with none it waits a little, like a short long poll.
func (f *fakeBotAPI) takeUpdates(ctx context.Context, token string) []telegram.Update {
	f.mu.Lock()
	updates := f.updates[token]
	delete(f.updates, token)
	f.mu.Unlock()

	if len(updates) == 0 {
		select {
		case <-ctx.Done():
		case <-time.After(10 * time.Millisecond):
		}

		return []telegram.Update{}
	}

	return updates
}

func writeResult(w http.ResponseWriter, result any) {
	writeAPI(w, http.StatusOK, map[string]any{"ok": true, "result": result})
}

func writeAPI(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// botToken is the token of a role's test bot: its bot ID is base + the position of the role.
func botToken(role gateway.Role, base int64) string {
	for i, r := range gateway.Roles {
		if r == role {
			return strconv.FormatInt(base+int64(i)+1, 10) + ":" + string(role) + "-SECRET"
		}
	}

	return ""
}

func botUsers(base int64) map[string]telegram.User {
	users := make(map[string]telegram.User, len(gateway.Roles))

	for i, role := range gateway.Roles {
		users[botToken(role, base)] = telegram.User{ID: base + int64(i) + 1, IsBot: true, Username: string(role) + "_bot"}
	}

	return users
}

func testBotToken(role gateway.Role) string {
	return botToken(role, 0)
}

func testBotUsers() map[string]telegram.User {
	return botUsers(0)
}

// gatewayConfig is an enabled gateway config against the fake server.
func gatewayConfig(t *testing.T, api *fakeBotAPI, base int64) *config.Telegram {
	t.Helper()

	dir := t.TempDir()

	return &config.Telegram{
		BotAPIURL:       api.baseURL,
		MarketerToken:   botToken(gateway.MarketerRole, base),
		CopywriterToken: botToken(gateway.CopywriterRole, base),
		EditorToken:     botToken(gateway.EditorRole, base),
		DesignerToken:   botToken(gateway.DesignerRole, base),
		GroupID:         api.groupID,
		AllowedUserIDs:  []int64{testUserID},
		PlanningTopicID: testTopicID,
		FilesDir:        dir,
		OutboxDir:       dir + "/outbox",
		MaxFileSize:     1 << 20,
		PollTimeout:     time.Second,
		RequestTimeout:  5 * time.Second,
		FileTimeout:     5 * time.Second,
		RetryBaseDelay:  time.Millisecond,
		RetryMaxDelay:   5 * time.Millisecond,
		Workers:         2,
	}
}

// testGatewaySender builds the real gateway.Sender through gateway.Module without starting the gateway
// (Redis is never contacted).
func testGatewaySender(t *testing.T, api *fakeBotAPI) *gateway.Sender {
	t.Helper()

	var sender *gateway.Sender

	fxtest.New(t,
		fx.Supply(gatewayConfig(t, api, 0), slog.New(slog.DiscardHandler), &config.Redis{Address: "127.0.0.1:1"}),
		redis.Module,
		gateway.Module,
		fx.Populate(&sender),
	)

	return sender
}
