package gateway

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/providers/telegram"
)

// Test tokens: the bot ID is the part before the colon.
const (
	marketerAuth   = "1:marketer-SECRET"
	copywriterAuth = "2:copywriter-SECRET"
	editorAuth     = "3:editor-SECRET"
	designerAuth   = "4:designer-SECRET"
)

type apiCall struct {
	token  string
	method string
	body   []byte
}

// fakeBotAPI is a Bot API server on httptest: getMe answers from identities, getUpdates hands out the
// queued updates once, getFile answers from files, send and edit methods are recorded.
type fakeBotAPI struct {
	mu         sync.Mutex
	identities map[string]telegram.User
	updates    map[string][]telegram.Update
	files      map[string]telegram.File
	calls      []apiCall
	// fileGate, when set, holds getFile until it is closed: a slow download.
	fileGate      chan struct{}
	nextMessageID int64
}

func newFakeBotAPI(t *testing.T) (api *fakeBotAPI, baseURL string) {
	t.Helper()

	api = &fakeBotAPI{
		identities: map[string]telegram.User{
			marketerAuth:   {ID: 1, IsBot: true, Username: "Marketer_Bot"},
			copywriterAuth: {ID: 2, IsBot: true, Username: "copy_bot"},
			editorAuth:     {ID: 3, IsBot: true, Username: "editor_bot"},
			designerAuth:   {ID: 4, IsBot: true, Username: "design_bot"},
		},
		updates:       map[string][]telegram.Update{},
		files:         map[string]telegram.File{},
		nextMessageID: 1000,
	}

	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)

	return api, srv.URL
}

func (f *fakeBotAPI) queue(token string, updates ...telegram.Update) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.updates[token] = append(f.updates[token], updates...)
}

// lastMessageID is the ID of the last message the fake "sent".
func (f *fakeBotAPI) lastMessageID() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.nextMessageID
}

func (f *fakeBotAPI) addFile(file telegram.File) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.files[file.FileID] = file
}

// callsOf returns the recorded calls of a method.
func (f *fakeBotAPI) callsOf(method string) []apiCall {
	f.mu.Lock()
	defer f.mu.Unlock()

	var calls []apiCall

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
	f.calls = append(f.calls, apiCall{token: token, method: method, body: body})
	identity, known := f.identities[token]
	f.mu.Unlock()

	if !known {
		writeError(w, http.StatusUnauthorized, "Unauthorized")

		return
	}

	switch method {
	case "getMe":
		writeResult(w, identity)
	case "getUpdates":
		writeResult(w, f.takeUpdates(r.Context(), token))
	case "getFile":
		f.mu.Lock()
		gate := f.fileGate
		f.mu.Unlock()

		if gate != nil {
			select {
			case <-gate:
			case <-r.Context().Done():
			}
		}

		var params struct {
			FileID string `json:"file_id"`
		}

		_ = json.Unmarshal(body, &params)

		f.mu.Lock()
		file, ok := f.files[params.FileID]
		f.mu.Unlock()

		if !ok {
			writeError(w, http.StatusBadRequest, "Bad Request: invalid file_id")

			return
		}

		writeResult(w, file)
	case "answerCallbackQuery":
		writeResult(w, true)
	default:
		f.mu.Lock()
		f.nextMessageID++
		id := f.nextMessageID
		f.mu.Unlock()

		writeResult(w, telegram.Message{MessageID: id, Chat: telegram.Chat{ID: testGroupID, Type: supergroupType}})
	}
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

func writeError(w http.ResponseWriter, status int, description string) {
	writeAPI(w, status, map[string]any{"ok": false, "error_code": status, "description": description})
}

func writeAPI(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// testConfig is an enabled gateway config against the fake server.
func testConfig(baseURL, filesDir string) *config.Telegram {
	return &config.Telegram{
		BotAPIURL:       baseURL,
		MarketerToken:   marketerAuth,
		CopywriterToken: copywriterAuth,
		EditorToken:     editorAuth,
		DesignerToken:   designerAuth,
		GroupID:         testGroupID,
		AllowedUserIDs:  []int64{testUserID},
		PlanningTopicID: planningTopic,
		SourcesTopicID:  sourcesTopic,
		FilesDir:        filesDir,
		OutboxDir:       filepath.Join(filesDir, "outbox"),
		MaxFileSize:     1 << 20,
		PollTimeout:     time.Second,
		RequestTimeout:  5 * time.Second,
		FileTimeout:     5 * time.Second,
		MaxRetries:      0,
		RetryBaseDelay:  time.Millisecond,
		RetryMaxDelay:   5 * time.Millisecond,
		Workers:         2,
	}
}

func testBots(t *testing.T, cfg *config.Telegram) *bots {
	t.Helper()

	b, err := newBots(cfg, slog.New(slog.DiscardHandler))
	require.NoError(t, err)

	return b
}

// memoryStore is the state store without Redis.
type memoryStore struct {
	mu       sync.Mutex
	offsets  map[int64]int64
	seen     map[string]bool
	unmarked []string
}

func newMemoryStore() *memoryStore {
	return &memoryStore{offsets: map[int64]int64{}, seen: map[string]bool{}}
}

func (s *memoryStore) Unmark(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.seen, key)
	s.unmarked = append(s.unmarked, key)

	return nil
}

func (s *memoryStore) unmarkedKeys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]string(nil), s.unmarked...)
}

func (s *memoryStore) LoadOffset(_ context.Context, botID int64) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.offsets[botID], nil
}

func (s *memoryStore) SaveOffset(_ context.Context, botID, offset int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.offsets[botID] = offset

	return nil
}

func (s *memoryStore) MarkSeen(_ context.Context, key string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.seen[key] {
		return false, nil
	}

	s.seen[key] = true

	return true, nil
}

func (s *memoryStore) offset(botID int64) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.offsets[botID]
}
