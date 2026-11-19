package embeddings

import (
	"context"
	"encoding/json"
	"log/slog"
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
	testModel        = "text-embedding-3-small"
	testAPIKey       = "key"
	retryAfterHeader = "Retry-After"
)

// fakeAPI answers with scripted statuses first, then with vectors whose first component is
// the position of the text in the whole Embed call (taken from the text itself).
type fakeAPI struct {
	mu        sync.Mutex
	statuses  []int
	errorCode string // "code" of scripted errors
	requests  []embeddingsRequest
	auth      []string
	dims      int
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	var req embeddingsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)

		return
	}

	f.requests = append(f.requests, req)
	f.auth = append(f.auth, r.Header.Get("Authorization"))

	if len(f.statuses) > 0 {
		status := f.statuses[0]
		f.statuses = f.statuses[1:]

		w.Header().Set(retryAfterHeader, "0")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]string{"message": "scripted failure", "code": f.errorCode},
		})

		return
	}

	dims := f.dims
	if dims == 0 {
		dims = Dimensions
	}

	type item struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	}

	// Reverse order: the client must sort by index.
	data := make([]item, 0, len(req.Input))
	for i := len(req.Input) - 1; i >= 0; i-- {
		vec := make([]float32, dims)
		vec[0] = float32(len(req.Input[i]))
		data = append(data, item{Index: i, Embedding: vec})
	}

	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}

// snapshot returns what the server has received; the lock orders it after the handler's writes.
func (f *fakeAPI) snapshot() (requests []embeddingsRequest, auth []string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.requests, f.auth
}

func newTestClient(t *testing.T, api *fakeAPI, apiKey string, batchSize int) *Client {
	t.Helper()

	return newTestClientWithModel(t, api, apiKey, batchSize, testModel)
}

func newTestClientWithModel(t *testing.T, api *fakeAPI, apiKey string, batchSize int, model string) *Client {
	t.Helper()

	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)

	return NewClient(
		&config.OpenAI{APIKey: apiKey, BaseURL: srv.URL + "/"},
		&config.Embeddings{
			Model:          model,
			BatchSize:      batchSize,
			MaxRetries:     2,
			RetryBaseDelay: time.Millisecond,
			Timeout:        5 * time.Second,
		},
		slog.New(slog.DiscardHandler),
	)
}

func TestEmbed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		apiKey       string
		statuses     []int
		dims         int
		input        []string
		expectErr    error
		expectFirsts []float32 // first component of each vector: the length of the text
		expectCalls  int
		errorCode    string
	}{
		{
			name:         "batches keep input order",
			apiKey:       testAPIKey,
			input:        []string{"a", "bb", "ccc", "dddd", "eeeee"},
			expectFirsts: []float32{1, 2, 3, 4, 5},
			expectCalls:  3,
		},
		{
			name:         "429 and 5xx are retried",
			apiKey:       testAPIKey,
			statuses:     []int{http.StatusTooManyRequests, http.StatusBadGateway},
			input:        []string{"a"},
			expectFirsts: []float32{1},
			expectCalls:  3,
		},
		{
			name:        "retries are limited",
			apiKey:      testAPIKey,
			statuses:    []int{http.StatusInternalServerError, http.StatusInternalServerError, http.StatusInternalServerError},
			input:       []string{"a"},
			expectErr:   ErrAPI,
			expectCalls: 3,
		},
		{
			name:        "4xx is not retried",
			apiKey:      testAPIKey,
			statuses:    []int{http.StatusBadRequest},
			input:       []string{"a"},
			expectErr:   ErrAPI,
			expectCalls: 1,
		},
		{
			name:        "wrong vector size",
			apiKey:      testAPIKey,
			dims:        3,
			input:       []string{"a"},
			expectErr:   ErrAPI,
			expectCalls: 1,
		},
		{
			name:        "429 insufficient_quota is not retried",
			apiKey:      testAPIKey,
			statuses:    []int{http.StatusTooManyRequests},
			errorCode:   insufficientQuotaCode,
			input:       []string{"a"},
			expectErr:   ErrAPI,
			expectCalls: 1,
		},
		{name: "no api key", input: []string{"a"}, expectErr: ErrNotConfigured},
		{name: "empty text", apiKey: testAPIKey, input: []string{"a", " "}, expectErr: ErrInvalidInput},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := &fakeAPI{statuses: tt.statuses, dims: tt.dims, errorCode: tt.errorCode}
			client := newTestClient(t, api, tt.apiKey, 2)

			got, err := client.Embed(context.Background(), tt.input)
			requests, auth := api.snapshot()

			assert.Len(t, requests, tt.expectCalls)

			if tt.expectErr != nil {
				require.ErrorIs(t, err, tt.expectErr)

				return
			}

			require.NoError(t, err)
			require.Len(t, got, len(tt.expectFirsts))

			for i, vec := range got {
				assert.Len(t, vec, Dimensions)
				assert.InDelta(t, tt.expectFirsts[i], vec[0], 0)
			}

			for i, req := range requests {
				assert.Equal(t, testModel, req.Model)
				assert.Equal(t, Dimensions, req.Dimensions)
				assert.Equal(t, encodingFloat, req.EncodingFormat)
				assert.Equal(t, "Bearer "+tt.apiKey, auth[i])
			}
		})
	}
}

func TestEmbedRequestOptions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		model        string
		batchSize    int
		inputs       int
		expectDims   int
		expectBatchN int
	}{
		{name: "v3 model gets dimensions", model: testModel, batchSize: 10, inputs: 1, expectDims: Dimensions, expectBatchN: 1},
		{name: "other model: no dimensions", model: "text-embedding-ada-002", batchSize: 10, inputs: 1, expectDims: 0, expectBatchN: 1},
		{name: "batches", model: testModel, batchSize: 2, inputs: 3, expectDims: Dimensions, expectBatchN: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := &fakeAPI{}
			client := newTestClientWithModel(t, api, testAPIKey, tt.batchSize, tt.model)

			inputs := make([]string, tt.inputs)
			for i := range inputs {
				inputs[i] = "x"
			}

			_, err := client.Embed(context.Background(), inputs)
			require.NoError(t, err)

			requests, _ := api.snapshot()
			require.Len(t, requests, tt.expectBatchN)
			assert.Equal(t, tt.expectDims, requests[0].Dimensions)
			assert.Equal(t, tt.model, requests[0].Model)
		})
	}
}

func TestEffectiveBatchSize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    int
		expected int
	}{
		{name: "zero", input: 0, expected: 1},
		{name: "kept", input: 100, expected: 100},
		{name: "capped", input: 5000, expected: maxBatchSize},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, effectiveBatchSize(tt.input))
		})
	}
}

func TestRetryAfter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		headers  map[string]string
		expected time.Duration
	}{
		{name: "none", expected: 0},
		{name: "milliseconds win", headers: map[string]string{"retry-after-ms": "1500", retryAfterHeader: "7"}, expected: 1500 * time.Millisecond},
		{name: "seconds", headers: map[string]string{retryAfterHeader: "2"}, expected: 2 * time.Second},
		{name: "garbage", headers: map[string]string{retryAfterHeader: "soon", "retry-after-ms": "x"}, expected: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := http.Header{}
			for k, v := range tt.headers {
				h.Set(k, v)
			}

			assert.Equal(t, tt.expected, retryAfter(h))
		})
	}
}

func TestEmbedBadBaseURLIsNotRetried(t *testing.T) {
	t.Parallel()

	client := NewClient(
		&config.OpenAI{APIKey: testAPIKey, BaseURL: "http://bad host"},
		&config.Embeddings{Model: testModel, BatchSize: 1, MaxRetries: 3, RetryBaseDelay: time.Hour},
		slog.New(slog.DiscardHandler),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := client.Embed(ctx, []string{"a"})

	require.Error(t, err)
	require.NotErrorIs(t, err, context.DeadlineExceeded, "a request-building error must fail at once")
}

func TestEmbedErrorMessage(t *testing.T) {
	t.Parallel()

	api := &fakeAPI{statuses: []int{http.StatusUnauthorized}}
	client := newTestClient(t, api, "bad-key", 2)

	_, err := client.Embed(context.Background(), []string{"a"})

	require.ErrorIs(t, err, ErrAPI)
	assert.Contains(t, err.Error(), "status 401: scripted failure")
}

func TestEmbedCanceledContext(t *testing.T) {
	t.Parallel()

	api := &fakeAPI{statuses: []int{http.StatusServiceUnavailable}}
	client := newTestClient(t, api, testAPIKey, 2)
	client.cfg.RetryBaseDelay = time.Hour

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := client.Embed(ctx, []string{"a"})

	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestBackoff(t *testing.T) {
	t.Parallel()

	client := &Client{cfg: config.Embeddings{RetryBaseDelay: time.Second}}

	tests := []struct {
		name     string
		attempt  int
		err      *statusError
		expected time.Duration
	}{
		{name: "first retry", attempt: 0, expected: time.Second},
		{name: "doubles", attempt: 3, expected: 8 * time.Second},
		{name: "capped", attempt: 50, expected: maxBackoff},
		{name: "retry-after wins", attempt: 3, err: &statusError{retryAfter: 2 * time.Second}, expected: 2 * time.Second},
		{name: "retry-after capped", attempt: 0, err: &statusError{retryAfter: time.Hour}, expected: maxBackoff},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, client.backoff(tt.attempt, tt.err))
		})
	}
}
