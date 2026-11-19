package whisper

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
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

	"github.com/Prokud1n/ai-agents/internal/config"
)

const (
	fixturePath = "testdata/verbose_json.json"
	testAPIKey  = "sk-test"
	audioBody   = "fake mp3 bytes"
	testModel   = "whisper-1"
)

// expectedForm is the form every request sends, plus the language and optional extra fields.
func expectedForm(language string, extra map[string][]string) map[string][]string {
	form := map[string][]string{
		"model":            {testModel},
		"response_format":  {responseFormat},
		granularitiesField: {wordGranularity, segmentGranularity},
		"language":         {language},
	}

	for k, v := range extra {
		form[k] = v
	}

	return form
}

func expectedFixture() *Transcript {
	return &Transcript{
		Language: "russian",
		Duration: 4.21,
		Text:     "Привет! Сегодня делаем присед.",
		Segments: []Segment{
			{ID: 0, Start: 0, End: 1.12, Text: " Привет!", NoSpeechProb: 0.012, AvgLogprob: -0.31, CompressionRatio: 0.82},
			{
				ID: 1, Start: 1.6, End: 3.94, Text: " Сегодня делаем присед.",
				NoSpeechProb: 0.004, AvgLogprob: -0.27, CompressionRatio: 0.91,
			},
		},
		Words: []Word{
			{Word: "Привет", Start: 0, End: 0.86},
			{Word: "Сегодня", Start: 1.6, End: 2.24},
			{Word: "делаем", Start: 2.24, End: 2.9},
			{Word: "присед", Start: 2.9, End: 3.94},
		},
	}
}

func TestParseTranscript(t *testing.T) {
	t.Parallel()

	fixture, err := os.ReadFile(fixturePath)
	require.NoError(t, err)

	tests := []struct {
		name      string
		input     []byte
		expected  *Transcript
		expectErr bool
	}{
		{name: "verbose_json fixture", input: fixture, expected: expectedFixture()},
		{name: "no words", input: []byte(`{"language":"russian","duration":1,"text":""}`),
			expected: &Transcript{Language: "russian", Duration: 1}},
		{name: "invalid json", input: []byte(`{"text":`), expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseTranscript(tt.input)
			if tt.expectErr {
				require.Error(t, err)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expected, got)
		})
	}
}

type fakeResponse struct {
	status  int
	body    string
	headers map[string]string
}

// fakeAPI answers with the given responses in order (the last one repeats) and records requests.
type fakeAPI struct {
	responses []fakeResponse

	mu       sync.Mutex
	calls    int
	lastForm map[string][]string
	lastFile string
	lastName string
	lastAuth string
	lastPath string
	parseErr error // checked by the test: assertions must not fail from the server goroutine
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	form, fileName, fileContent, err := readMultipart(r)

	f.mu.Lock()
	f.parseErr = errors.Join(f.parseErr, err)
	idx := min(f.calls, len(f.responses)-1)
	f.calls++
	f.lastForm = form
	f.lastName = fileName
	f.lastFile = fileContent
	f.lastAuth = r.Header.Get("Authorization")
	f.lastPath = r.Method + " " + r.URL.Path
	f.mu.Unlock()

	resp := f.responses[idx]
	for k, v := range resp.headers {
		w.Header().Set(k, v)
	}

	w.WriteHeader(resp.status)
	if _, err := w.Write([]byte(resp.body)); err != nil {
		f.mu.Lock()
		f.parseErr = errors.Join(f.parseErr, err)
		f.mu.Unlock()
	}
}

// readMultipart streams the request parts: form values, and the name and content of the "file" part.
func readMultipart(r *http.Request) (form map[string][]string, fileName, fileContent string, err error) {
	form = map[string][]string{}

	mr, err := r.MultipartReader()
	if err != nil {
		return nil, "", "", fmt.Errorf("failed to read multipart: %w", err)
	}

	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			return form, fileName, fileContent, nil
		}

		if err != nil {
			return nil, "", "", fmt.Errorf("failed to read part: %w", err)
		}

		content, err := io.ReadAll(part)
		if err != nil {
			return nil, "", "", fmt.Errorf("failed to read part %s: %w", part.FormName(), err)
		}

		if part.FormName() == "file" {
			fileName, fileContent = part.FileName(), string(content)

			continue
		}

		form[part.FormName()] = append(form[part.FormName()], string(content))
	}
}

func newTestClient(t *testing.T, baseURL, apiKey string) *Client {
	t.Helper()

	return newTestClientWithTimeout(t, baseURL, apiKey, 5*time.Second)
}

func newTestClientWithTimeout(t *testing.T, baseURL, apiKey string, timeout time.Duration) *Client {
	t.Helper()

	return NewClient(
		&config.OpenAI{APIKey: apiKey, BaseURL: baseURL + "/"},
		&config.Whisper{
			Model:          testModel,
			Language:       "ru",
			RequestTimeout: timeout,
			MaxRetries:     2,
			RetryBaseDelay: time.Millisecond,
		},
		slog.New(slog.DiscardHandler),
	)
}

func TestClientTranscribe(t *testing.T) {
	t.Parallel()

	fixture, err := os.ReadFile(fixturePath)
	require.NoError(t, err)

	ok := fakeResponse{status: http.StatusOK, body: string(fixture)}
	rateLimited := fakeResponse{
		status:  http.StatusTooManyRequests,
		body:    `{"error":{"message":"Rate limit reached","type":"requests","code":"rate_limit_exceeded"}}`,
		headers: map[string]string{"Retry-After": "0"},
	}
	noQuota := fakeResponse{
		status: http.StatusTooManyRequests,
		body:   `{"error":{"message":"You exceeded your current quota","code":"insufficient_quota"}}`,
	}
	serverError := fakeResponse{status: http.StatusBadGateway, body: "bad gateway"}
	badRequest := fakeResponse{
		status: http.StatusBadRequest,
		body:   `{"error":{"message":"Invalid file format.","code":null}}`,
	}

	tests := []struct {
		name          string
		apiKey        string
		responses     []fakeResponse
		input         TranscribeData
		expectedCalls int
		expectedErr   error
		expectedForm  map[string][]string
		expectedInErr string
	}{
		{
			name:          "multipart request with word and segment timestamps",
			apiKey:        testAPIKey,
			responses:     []fakeResponse{ok},
			input:         TranscribeData{Prompt: "предыдущий кусок"},
			expectedCalls: 1,
			expectedForm:  expectedForm("ru", map[string][]string{"prompt": {"предыдущий кусок"}}),
		},
		{
			name:          "language from data overrides the default",
			apiKey:        testAPIKey,
			responses:     []fakeResponse{ok},
			input:         TranscribeData{Language: "en"},
			expectedCalls: 1,
			expectedForm:  expectedForm("en", nil),
		},
		{
			name:          "retries on 429",
			apiKey:        testAPIKey,
			responses:     []fakeResponse{rateLimited, ok},
			expectedCalls: 2,
		},
		{
			name:          "retries on 5xx until attempts run out",
			apiKey:        testAPIKey,
			responses:     []fakeResponse{serverError},
			expectedCalls: 3,
			expectedInErr: "openai status 502: bad gateway",
		},
		{
			name:          "insufficient quota is not retried",
			apiKey:        testAPIKey,
			responses:     []fakeResponse{noQuota},
			expectedCalls: 1,
			expectedInErr: "insufficient_quota",
		},
		{
			name:          "client error is not retried",
			apiKey:        testAPIKey,
			responses:     []fakeResponse{badRequest},
			expectedCalls: 1,
			expectedInErr: "Invalid file format.",
		},
		{
			name:          "no api key",
			responses:     []fakeResponse{ok},
			expectedCalls: 0,
			expectedErr:   ErrAPIKeyMissing,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := &fakeAPI{responses: tt.responses}
			server := httptest.NewServer(api)
			t.Cleanup(server.Close)

			audio := filepath.Join(t.TempDir(), "chunk-000.mp3")
			require.NoError(t, os.WriteFile(audio, []byte(audioBody), 0o600))

			data := tt.input
			data.Path = audio

			got, err := newTestClient(t, server.URL, tt.apiKey).Transcribe(t.Context(), data)

			api.mu.Lock()
			defer api.mu.Unlock()

			require.NoError(t, api.parseErr)
			assert.Equal(t, tt.expectedCalls, api.calls)

			switch {
			case tt.expectedErr != nil:
				require.ErrorIs(t, err, tt.expectedErr)
				assert.Nil(t, got)
			case tt.expectedInErr != "":
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedInErr)
				assert.Nil(t, got)
			default:
				require.NoError(t, err)
				assert.Equal(t, expectedFixture(), got)
				assert.Equal(t, "POST /audio/transcriptions", api.lastPath)
				assert.Equal(t, "Bearer "+testAPIKey, api.lastAuth)
				assert.Equal(t, "chunk-000.mp3", api.lastName)
				assert.Equal(t, audioBody, api.lastFile)
			}

			if tt.expectedForm != nil {
				assert.Equal(t, tt.expectedForm, api.lastForm)
			}
		})
	}
}

// TestClientRetrySafety: a request that may have reached the API is not repeated, so it is not billed twice.
func TestClientRetrySafety(t *testing.T) {
	t.Parallel()

	var slowCalls, droppedCalls atomic.Int32

	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slowCalls.Add(1)

		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			return
		}

		select {
		case <-r.Context().Done():
		case <-time.After(time.Second):
		}

		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(slow.Close)

	// Reads the whole body, then drops the connection without an answer.
	dropped := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		droppedCalls.Add(1)

		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			return
		}

		conn, _, err := http.NewResponseController(w).Hijack()
		if err != nil {
			return
		}

		if err := conn.Close(); err != nil {
			return
		}
	}))
	t.Cleanup(dropped.Close)

	refused := httptest.NewServer(http.NotFoundHandler())
	refusedURL := refused.URL
	refused.Close()

	tests := []struct {
		name          string
		url           string
		timeout       time.Duration
		calls         *atomic.Int32
		expectedCalls int32
		expectedInErr string
	}{
		{
			name: "request timeout is not retried", url: slow.URL, timeout: 50 * time.Millisecond,
			calls: &slowCalls, expectedCalls: 1, expectedInErr: "after 1 attempt(s)",
		},
		{
			name: "connection lost after the body was sent is not retried", url: dropped.URL, timeout: 5 * time.Second,
			calls: &droppedCalls, expectedCalls: 1, expectedInErr: "after 1 attempt(s)",
		},
		{
			name: "connection refused before sending is retried", url: refusedURL, timeout: 5 * time.Second,
			expectedInErr: "after 3 attempt(s)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			audio := filepath.Join(t.TempDir(), "chunk.mp3")
			require.NoError(t, os.WriteFile(audio, []byte(audioBody), 0o600))

			client := newTestClientWithTimeout(t, tt.url, testAPIKey, tt.timeout)

			_, err := client.Transcribe(t.Context(), TranscribeData{Path: audio})

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.expectedInErr)

			if tt.calls != nil {
				assert.Equal(t, tt.expectedCalls, tt.calls.Load())
			}
		})
	}
}

func TestClientTranscribeFileErrors(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	large := filepath.Join(dir, "large.mp3")
	require.NoError(t, os.WriteFile(large, make([]byte, MaxFileSize+1), 0o600))

	tests := []struct {
		name        string
		input       string
		expectedErr error
	}{
		{name: "missing file", input: filepath.Join(dir, "missing.mp3"), expectedErr: os.ErrNotExist},
		{name: "file over the upload limit", input: large, expectedErr: ErrFileTooLarge},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client := newTestClient(t, "http://127.0.0.1:1", testAPIKey)

			_, err := client.Transcribe(t.Context(), TranscribeData{Path: tt.input})

			require.ErrorIs(t, err, tt.expectedErr)
		})
	}
}

func TestRetryDelay(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		attempt  int
		after    time.Duration
		expected time.Duration
	}{
		{name: "first retry", attempt: 0, expected: 2 * time.Second},
		{name: "exponential", attempt: 3, expected: 16 * time.Second},
		{name: "capped", attempt: 10, expected: time.Minute},
		{name: "retry-after is longer", attempt: 0, after: 7 * time.Second, expected: 7 * time.Second},
		{name: "retry-after is capped", attempt: 0, after: time.Hour, expected: time.Minute},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, retryDelay(tt.attempt, 2*time.Second, tt.after))
		})
	}
}

func TestParseRetryAfter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected time.Duration
	}{
		{name: "empty", input: "", expected: 0},
		{name: "seconds", input: "3", expected: 3 * time.Second},
		{name: "fractional", input: "0.5", expected: 500 * time.Millisecond},
		{name: "http date is ignored", input: "Wed, 21 Oct 2026 07:28:00 GMT", expected: 0},
		{name: "negative", input: "-1", expected: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, parseRetryAfter(tt.input))
		})
	}
}
