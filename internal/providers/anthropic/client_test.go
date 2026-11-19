package anthropic

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/config"
)

const (
	testRequestID = "req_test"
	fakeAuth      = "test-auth"
)

const okBody = `{"id":"msg_1","model":"m","role":"assistant","stop_reason":"end_turn",` +
	`"content":[{"type":"text","text":"Hi"},{"type":"thinking","thinking":"t","signature":"sig"}],` +
	`"usage":{"input_tokens":3,"output_tokens":2,"cache_read_input_tokens":1,"cache_creation_input_tokens":4}}`

type scriptedResponse struct {
	status      int
	retryAfter  string
	shouldRetry string
	body        string
}

type scriptedServer struct {
	mu        sync.Mutex
	responses []scriptedResponse
	headers   []http.Header
}

func (s *scriptedServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, _ = io.Copy(io.Discard, r.Body)
	s.headers = append(s.headers, r.Header.Clone())

	resp := s.responses[min(len(s.headers), len(s.responses))-1]
	if resp.retryAfter != "" {
		w.Header().Set("retry-after", resp.retryAfter)
	}

	if resp.shouldRetry != "" {
		w.Header().Set(shouldRetryHeader, resp.shouldRetry)
	}

	w.Header().Set("request-id", testRequestID)
	w.WriteHeader(resp.status)
	_, _ = io.WriteString(w, resp.body)
}

func (s *scriptedServer) requestHeaders() []http.Header {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]http.Header(nil), s.headers...)
}

func newTestClient(t *testing.T, apiKey string, server *scriptedServer) *Client {
	t.Helper()

	srv := httptest.NewServer(server)
	t.Cleanup(srv.Close)

	return NewClient(&config.Anthropic{
		APIKey:         apiKey,
		BaseURL:        srv.URL + "/",
		Timeout:        5 * time.Second,
		MaxRetries:     2,
		RetryBaseDelay: time.Millisecond,
		RetryMaxDelay:  5 * time.Millisecond,
	}, slog.New(slog.DiscardHandler))
}

func testRequest() *MessageRequest {
	return &MessageRequest{
		Model:     "m",
		MaxTokens: 10,
		Messages:  []Message{{Role: UserRole, Content: []ContentBlock{TextBlock("Hello")}}},
	}
}

func TestCreateMessage(t *testing.T) {
	t.Parallel()

	rateLimited := scriptedResponse{
		status: http.StatusTooManyRequests, retryAfter: "0",
		body: `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`,
	}
	overloaded := scriptedResponse{
		status: 529, body: `{"type":"error","error":{"type":"overloaded_error","message":"overloaded"}}`,
	}
	badRequest := scriptedResponse{
		status: http.StatusBadRequest,
		body:   `{"type":"error","error":{"type":"invalid_request_error","message":"bad"}}`,
	}
	ok := scriptedResponse{status: http.StatusOK, body: okBody}

	tests := []struct {
		name           string
		responses      []scriptedResponse
		expectedCalls  int
		expectedStatus int // 0: success
		expectedType   string
	}{
		{name: "success", responses: []scriptedResponse{ok}, expectedCalls: 1},
		{name: "429 then success", responses: []scriptedResponse{rateLimited, ok}, expectedCalls: 2},
		{name: "529 then success", responses: []scriptedResponse{overloaded, overloaded, ok}, expectedCalls: 3},
		{
			name: "retries exhausted", responses: []scriptedResponse{rateLimited},
			expectedCalls: 3, expectedStatus: http.StatusTooManyRequests, expectedType: "rate_limit_error",
		},
		{
			name: "400 is not retried", responses: []scriptedResponse{badRequest, ok},
			expectedCalls: 1, expectedStatus: http.StatusBadRequest, expectedType: "invalid_request_error",
		},
		{
			name: "non-json error body", responses: []scriptedResponse{{status: http.StatusForbidden, body: "denied"}},
			expectedCalls: 1, expectedStatus: http.StatusForbidden,
		},
		{
			name: "x-should-retry true retries a 409",
			responses: []scriptedResponse{
				{status: http.StatusConflict, shouldRetry: "true", body: `{"error":{"type":"conflict","message":"x"}}`},
				ok,
			},
			expectedCalls: 2,
		},
		{
			name: "x-should-retry false stops a 500",
			responses: []scriptedResponse{
				{status: http.StatusInternalServerError, shouldRetry: "false", body: `{"error":{"type":"api_error","message":"x"}}`},
				ok,
			},
			expectedCalls: 1, expectedStatus: http.StatusInternalServerError, expectedType: "api_error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := &scriptedServer{responses: tt.responses}
			client := newTestClient(t, fakeAuth, server)

			resp, err := client.CreateMessage(context.Background(), testRequest())

			headers := server.requestHeaders()
			require.Len(t, headers, tt.expectedCalls)
			assert.Equal(t, fakeAuth, headers[0].Get(authHeader))
			assert.Equal(t, apiVersion, headers[0].Get(versionHeader))

			if tt.expectedStatus != 0 {
				var apiErr *APIError

				require.ErrorAs(t, err, &apiErr)
				assert.Equal(t, tt.expectedStatus, apiErr.StatusCode)
				assert.Equal(t, tt.expectedType, apiErr.Type)
				assert.Equal(t, testRequestID, apiErr.RequestID)
				assert.Nil(t, resp)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, "Hi", resp.Text())
			assert.Equal(t, EndTurnStopReason, resp.StopReason)
			assert.Equal(t, testRequestID, resp.RequestID)
			assert.Equal(t, Usage{
				InputTokens: 3, OutputTokens: 2, CacheReadInputTokens: 1, CacheCreationInputTokens: 4,
			}, resp.Usage)
		})
	}
}

func TestCreateMessageWithoutKey(t *testing.T) {
	t.Parallel()

	server := &scriptedServer{responses: []scriptedResponse{{status: http.StatusOK, body: okBody}}}
	client := newTestClient(t, "", server)

	resp, err := client.CreateMessage(context.Background(), testRequest())

	require.ErrorIs(t, err, ErrAPIKeyMissing)
	assert.Nil(t, resp)
	assert.Empty(t, server.requestHeaders())
	assert.False(t, client.Configured())
}

func TestCreateMessageCancelled(t *testing.T) {
	t.Parallel()

	server := &scriptedServer{responses: []scriptedResponse{{status: http.StatusServiceUnavailable, body: "down"}}}
	client := newTestClient(t, fakeAuth, server)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := client.CreateMessage(ctx, testRequest())

	require.ErrorIs(t, err, context.Canceled)
}

// Blocks decoded from a response are sent back byte for byte, so unknown block types survive the next turn.
func TestContentBlockRoundTrip(t *testing.T) {
	t.Parallel()

	var resp MessageResponse
	require.NoError(t, json.Unmarshal([]byte(okBody), &resp))
	require.Len(t, resp.Content, 2)

	data, err := json.Marshal(&MessageRequest{Messages: []Message{{Role: AssistantRole, Content: resp.Content}}})
	require.NoError(t, err)

	assert.JSONEq(t,
		`{"model":"","max_tokens":0,"messages":[{"role":"assistant","content":[`+
			`{"type":"text","text":"Hi"},{"type":"thinking","thinking":"t","signature":"sig"}]}]}`,
		string(data))

	built, err := json.Marshal([]ContentBlock{ToolResultBlock("toolu_1", "failed", true)})
	require.NoError(t, err)
	assert.JSONEq(t, `[{"type":"tool_result","tool_use_id":"toolu_1","content":"failed","is_error":true}]`, string(built))
}

func TestBackoff(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		attempt  int
		expected time.Duration
	}{
		{name: "first retry", attempt: 0, expected: time.Second},
		{name: "doubles", attempt: 2, expected: 4 * time.Second},
		{name: "capped", attempt: 10, expected: 30 * time.Second},
		{name: "huge attempt does not overflow", attempt: 1000, expected: 30 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, backoff(time.Second, 30*time.Second, tt.attempt))
		})
	}
}

func TestEqualJitter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input time.Duration
	}{
		{name: "zero", input: 0},
		{name: "one nanosecond", input: 1},
		{name: "second", input: time.Second},
		{name: "minute", input: time.Minute},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			for range 100 {
				got := equalJitter(tt.input)
				assert.GreaterOrEqual(t, got, tt.input/2)
				assert.LessOrEqual(t, got, tt.input)
			}
		})
	}
}

func TestParseShouldRetry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected *bool
	}{
		{name: "retry", input: "true", expected: new(true)},
		{name: "do not retry, upper case", input: " FALSE ", expected: new(false)},
		{name: "absent", input: "", expected: nil},
		{name: "garbage", input: "maybe", expected: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, parseShouldRetry(tt.input))
		})
	}
}

// A timeout is not retried: the server may have processed (and billed) the request.
func TestCreateMessageTimeoutIsNotRetried(t *testing.T) {
	t.Parallel()

	var (
		mu    sync.Mutex
		calls int
	)

	release := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()

		select {
		case <-r.Context().Done():
		case <-release:
		}

		w.WriteHeader(http.StatusOK)
	}))
	// Cleanups run in reverse order: the handler is released before the server closes.
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })

	client := NewClient(&config.Anthropic{
		APIKey:         fakeAuth,
		BaseURL:        srv.URL,
		Timeout:        50 * time.Millisecond,
		MaxRetries:     2,
		RetryBaseDelay: time.Millisecond,
		RetryMaxDelay:  5 * time.Millisecond,
	}, slog.New(slog.DiscardHandler))

	_, err := client.CreateMessage(context.Background(), testRequest())

	var netErr net.Error

	require.ErrorAs(t, err, &netErr)
	assert.True(t, netErr.Timeout())

	mu.Lock()
	defer mu.Unlock()

	assert.Equal(t, 1, calls)
}

func TestParseRetryAfter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected time.Duration
	}{
		{name: "seconds", input: "12", expected: 12 * time.Second},
		{name: "spaces", input: " 3 ", expected: 3 * time.Second},
		{name: "empty", input: "", expected: 0},
		{name: "negative", input: "-1", expected: 0},
		{name: "http date is ignored", input: "Wed, 21 Oct 2015 07:28:00 GMT", expected: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, parseRetryAfter(tt.input))
		})
	}
}
