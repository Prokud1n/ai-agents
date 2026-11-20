package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testToken = "123456:SECRET-token_value"

type scriptedResponse struct {
	status int
	body   string
}

type scriptedServer struct {
	mu        sync.Mutex
	responses []scriptedResponse
	paths     []string
	bodies    [][]byte
}

func (s *scriptedServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	body, _ := io.ReadAll(r.Body)
	s.paths = append(s.paths, r.URL.Path)
	s.bodies = append(s.bodies, body)

	resp := s.responses[min(len(s.paths), len(s.responses))-1]
	w.Header().Set("Content-Type", jsonContentType)
	w.WriteHeader(resp.status)
	_, _ = io.WriteString(w, resp.body)
}

func (s *scriptedServer) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return len(s.paths)
}

type recordedSleeps struct {
	mu     sync.Mutex
	delays []time.Duration
}

func (r *recordedSleeps) sleep(_ context.Context, d time.Duration) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.delays = append(r.delays, d)

	return nil
}

func newTestClient(t *testing.T, baseURL string, log *slog.Logger) (*Client, *recordedSleeps) {
	t.Helper()

	client := NewClient(&Options{
		BaseURL:        baseURL,
		Token:          testToken,
		RequestTimeout: 5 * time.Second,
		FileTimeout:    5 * time.Second,
		MaxRetries:     2,
		RetryBaseDelay: time.Millisecond,
		RetryMaxDelay:  10 * time.Millisecond,
	}, log)

	sleeps := &recordedSleeps{}
	client.sleep = sleeps.sleep

	return client, sleeps
}

func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

func TestBotIDFromToken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected int64
		wantErr  bool
	}{
		{name: "valid", input: testToken, expected: 123456},
		{name: "empty", input: "", wantErr: true},
		{name: "no secret", input: "123456:", wantErr: true},
		{name: "no colon", input: "123456", wantErr: true},
		{name: "not a number", input: "abc:secret", wantErr: true},
		{name: "negative", input: "-5:secret", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			id, err := BotIDFromToken(tt.input)
			if tt.wantErr {
				require.ErrorIs(t, err, ErrInvalidToken)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expected, id)
		})
	}
}

func TestClientRetries(t *testing.T) {
	t.Parallel()

	const okMe = `{"ok":true,"result":{"id":123456,"is_bot":true,"first_name":"M","username":"marketer_bot"}}`

	tests := []struct {
		name           string
		responses      []scriptedResponse
		expectedCalls  int
		expectedSleeps []time.Duration // exact delays, checked when set
		expectedCode   int             // APIError code of the final error; 0 means success
	}{
		{
			name: "429 waits retry_after",
			responses: []scriptedResponse{
				{status: http.StatusTooManyRequests, body: `{"ok":false,"error_code":429,"description":"Too Many Requests","parameters":{"retry_after":3}}`},
				{status: http.StatusOK, body: okMe},
			},
			expectedCalls:  2,
			expectedSleeps: []time.Duration{3 * time.Second},
		},
		{
			name: "5xx is retried",
			responses: []scriptedResponse{
				{status: http.StatusBadGateway, body: `<html>bad gateway</html>`},
				{status: http.StatusInternalServerError, body: `{"ok":false,"error_code":500,"description":"Internal"}`},
				{status: http.StatusOK, body: okMe},
			},
			expectedCalls: 3,
		},
		{
			name: "4xx is not retried",
			responses: []scriptedResponse{
				{status: http.StatusBadRequest, body: `{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`},
			},
			expectedCalls: 1,
			expectedCode:  http.StatusBadRequest,
		},
		{
			name: "retries are limited",
			responses: []scriptedResponse{
				{status: http.StatusTooManyRequests, body: `{"ok":false,"error_code":429,"description":"Too Many Requests","parameters":{"retry_after":1}}`},
			},
			expectedCalls: 3,
			expectedCode:  http.StatusTooManyRequests,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := &scriptedServer{responses: tt.responses}
			srv := httptest.NewServer(server)
			t.Cleanup(srv.Close)

			client, sleeps := newTestClient(t, srv.URL, discardLogger())

			user, err := client.GetMe(t.Context())

			assert.Equal(t, tt.expectedCalls, server.calls())

			if tt.expectedSleeps != nil {
				assert.Equal(t, tt.expectedSleeps, sleeps.delays)
			}

			if tt.expectedCode != 0 {
				var apiErr *APIError
				require.ErrorAs(t, err, &apiErr)
				assert.Equal(t, tt.expectedCode, apiErr.Code)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, "marketer_bot", user.Username)
			assert.Equal(t, "/bot"+testToken+"/getMe", server.paths[0])
		})
	}
}

func TestClientMasksToken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		baseURL func(t *testing.T) string
	}{
		{
			name: "connection refused",
			baseURL: func(t *testing.T) string {
				t.Helper()

				srv := httptest.NewServer(http.NotFoundHandler())
				srv.Close()

				return srv.URL
			},
		},
		{
			name: "server closes the connection",
			baseURL: func(t *testing.T) string {
				t.Helper()

				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					conn, _, err := w.(http.Hijacker).Hijack()
					if err == nil {
						_ = conn.Close()
					}
				}))
				t.Cleanup(srv.Close)

				return srv.URL
			},
		},
		{
			name: "invalid url",
			baseURL: func(*testing.T) string {
				return "http://bad host"
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var logs bytes.Buffer

			log := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
			client, _ := newTestClient(t, tt.baseURL(t), log)

			_, err := client.GetMe(t.Context())

			require.Error(t, err)
			assert.NotContains(t, err.Error(), "SECRET")
			assert.NotContains(t, logs.String(), "SECRET")
		})
	}
}

func TestMaskToken(t *testing.T) {
	t.Parallel()

	client, _ := newTestClient(t, "http://localhost", discardLogger())

	masked := client.MaskToken("/var/lib/telegram-bot-api/" + testToken + "/videos/file_1.mp4")

	assert.Equal(t, "/var/lib/telegram-bot-api/<token>/videos/file_1.mp4", masked)
}

func TestGetUpdatesSendsParams(t *testing.T) {
	t.Parallel()

	server := &scriptedServer{responses: []scriptedResponse{{
		status: http.StatusOK,
		body:   `{"ok":true,"result":[{"update_id":42,"message":{"message_id":7,"date":1,"chat":{"id":-100,"type":"supergroup"},"text":"hi"}}]}`,
	}}}
	srv := httptest.NewServer(server)
	t.Cleanup(srv.Close)

	client, _ := newTestClient(t, srv.URL, discardLogger())

	updates, err := client.GetUpdates(t.Context(), &GetUpdatesParams{
		Offset: 41, Timeout: 1, AllowedUpdates: []string{CallbackQueryUpdate},
	})

	require.NoError(t, err)
	require.Len(t, updates, 1)
	assert.Equal(t, int64(42), updates[0].UpdateID)
	assert.Equal(t, "hi", updates[0].Message.Text)
	assert.JSONEq(t, `{"offset":41,"timeout":1,"allowed_updates":["callback_query"]}`, string(server.bodies[0]))
}

func TestSendVideoUploadsFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "draft.mp4")
	require.NoError(t, os.WriteFile(path, []byte("video-bytes"), 0o600))

	type upload struct {
		contentLength int64
		fields        map[string]string
		fileName      string
		fileData      string
	}

	uploads := make(chan upload, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := upload{contentLength: r.ContentLength, fields: map[string]string{}}

		_, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		reader := multipart.NewReader(r.Body, params["boundary"])

		for {
			part, err := reader.NextPart()
			if err != nil {
				break
			}

			data, _ := io.ReadAll(part)
			if part.FormName() == "video" {
				got.fileName = part.FileName()
				got.fileData = string(data)

				continue
			}

			got.fields[part.FormName()] = string(data)
		}

		uploads <- got

		_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":99,"date":1,"chat":{"id":-100,"type":"supergroup"}}}`)
	}))
	t.Cleanup(srv.Close)

	client, _ := newTestClient(t, srv.URL, discardLogger())

	file, err := os.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = file.Close() })

	msg, err := client.SendVideo(t.Context(), &SendFileParams{
		ChatID:          -100,
		MessageThreadID: 5,
		File:            file,
		FileName:        "draft.mp4",
		Caption:         "Черновик",
		ReplyParameters: &ReplyParameters{MessageID: 7},
		ReplyMarkup: &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{
			{{Text: "Одобрить", CallbackData: "approve:1"}},
		}},
	})

	require.NoError(t, err)
	assert.Equal(t, int64(99), msg.MessageID)

	got := <-uploads
	assert.Positive(t, got.contentLength)
	assert.Equal(t, "draft.mp4", got.fileName)
	assert.Equal(t, "video-bytes", got.fileData)
	assert.Equal(t, "-100", got.fields["chat_id"])
	assert.Equal(t, "5", got.fields["message_thread_id"])
	assert.Equal(t, "Черновик", got.fields["caption"])
	assert.JSONEq(t, `{"message_id":7}`, got.fields["reply_parameters"])

	var markup InlineKeyboardMarkup
	require.NoError(t, json.Unmarshal([]byte(got.fields["reply_markup"]), &markup))
	assert.Equal(t, "approve:1", markup.InlineKeyboard[0][0].CallbackData)
}

func TestSendVideoWithoutFile(t *testing.T) {
	t.Parallel()

	client, _ := newTestClient(t, "http://127.0.0.1:1", discardLogger())

	_, err := client.SendVideo(t.Context(), &SendFileParams{ChatID: -100})

	require.ErrorIs(t, err, errNoFile)
}

func TestClientDoesNotFollowRedirects(t *testing.T) {
	t.Parallel()

	var leaked atomic.Int32

	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		leaked.Add(1)
	}))
	t.Cleanup(other.Close)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/redirected", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(srv.Close)

	client, _ := newTestClient(t, srv.URL, discardLogger())

	_, err := client.GetMe(t.Context())

	var apiErr *APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusTemporaryRedirect, apiErr.Code)
	assert.Zero(t, leaked.Load(), "the token must not reach another host")
}
