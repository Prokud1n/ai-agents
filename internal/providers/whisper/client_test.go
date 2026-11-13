package whisper

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
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
			{ID: 0, Start: 0, End: 1.12, Text: " Привет!", NoSpeechProb: 0.012},
			{ID: 1, Start: 1.6, End: 3.94, Text: " Сегодня делаем присед.", NoSpeechProb: 0.004},
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
	t         *testing.T
	responses []fakeResponse

	mu       sync.Mutex
	calls    int
	lastForm map[string][]string
	lastFile string
	lastName string
	lastAuth string
	lastPath string
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	err := r.ParseMultipartForm(1 << 20)
	assert.NoError(f.t, err)

	f.mu.Lock()
	idx := min(f.calls, len(f.responses)-1)
	f.calls++
	f.lastForm = r.MultipartForm.Value
	f.lastAuth = r.Header.Get("Authorization")
	f.lastPath = r.Method + " " + r.URL.Path

	if files := r.MultipartForm.File["file"]; len(files) == 1 {
		f.lastName = files[0].Filename

		file, err := files[0].Open()
		assert.NoError(f.t, err)

		content, err := io.ReadAll(file)
		assert.NoError(f.t, err)
		assert.NoError(f.t, file.Close())

		f.lastFile = string(content)
	}
	f.mu.Unlock()

	resp := f.responses[idx]
	for k, v := range resp.headers {
		w.Header().Set(k, v)
	}

	w.WriteHeader(resp.status)
	_, err = w.Write([]byte(resp.body))
	assert.NoError(f.t, err)
}

func newTestClient(t *testing.T, baseURL, apiKey string) *Client {
	t.Helper()

	return NewClient(
		&config.OpenAI{APIKey: apiKey, BaseURL: baseURL + "/"},
		&config.Whisper{
			Model:          testModel,
			Language:       "ru",
			RequestTimeout: 5 * time.Second,
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

			api := &fakeAPI{t: t, responses: tt.responses}
			server := httptest.NewServer(api)
			t.Cleanup(server.Close)

			audio := filepath.Join(t.TempDir(), "chunk-000.mp3")
			require.NoError(t, os.WriteFile(audio, []byte(audioBody), 0o600))

			data := tt.input
			data.Path = audio

			got, err := newTestClient(t, server.URL, tt.apiKey).Transcribe(t.Context(), data)

			api.mu.Lock()
			defer api.mu.Unlock()

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
