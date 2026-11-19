// Package embeddings is a client for the OpenAI Embeddings API (or any API compatible with it).
package embeddings

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Prokud1n/ai-agents/internal/config"
)

// Dimensions is the vector size stored in the database (migrations/003_knowledge_base.up.sql).
const Dimensions = 1536

const (
	embeddingsPath   = "/embeddings"
	encodingFloat    = "float"
	maxErrorBodySize = 4 << 10
	maxBackoff       = 30 * time.Second
	maxBatchSize     = 2048 // inputs per request allowed by the API
	float64Bits      = 64

	// Only the v3 models accept "dimensions"; for others it is an error.
	dimensionsModelPrefix = "text-embedding-3-"
	// 429 with this code means the account is out of credit: retrying does not help.
	insufficientQuotaCode = "insufficient_quota"
)

var (
	// ErrNotConfigured is returned on every call when OPENAI_API_KEY is empty: the app starts without
	// the key, and only the features that need embeddings fail.
	ErrNotConfigured = errors.New("embeddings are not configured: set OPENAI_API_KEY")
	ErrInvalidInput  = errors.New("invalid embeddings input")
	ErrAPI           = errors.New("embeddings API error")

	// errTransport marks failures to get any response: they are retried, unlike request-building errors.
	errTransport = errors.New("embeddings transport error")
)

type Client struct {
	apiKey  string
	baseURL string
	cfg     config.Embeddings
	http    *http.Client
	log     *slog.Logger
}

func NewClient(openAI *config.OpenAI, cfg *config.Embeddings, log *slog.Logger) *Client {
	return &Client{
		apiKey:  openAI.APIKey,
		baseURL: strings.TrimRight(openAI.BaseURL, "/"),
		cfg:     *cfg,
		http:    &http.Client{Timeout: cfg.Timeout},
		log:     log,
	}
}

// Model is the embeddings model name; vectors of different models are not comparable.
func (c *Client) Model() string {
	return c.cfg.Model
}

// Embed returns one vector per text, in the same order. Texts are sent in batches of EMBEDDINGS_BATCH_SIZE;
// 429 and 5xx responses are retried with exponential backoff (honouring Retry-After).
func (c *Client) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if c.apiKey == "" {
		return nil, ErrNotConfigured
	}

	for i, t := range texts {
		if strings.TrimSpace(t) == "" {
			return nil, fmt.Errorf("text %d is empty: %w", i, ErrInvalidInput)
		}
	}

	batchSize := effectiveBatchSize(c.cfg.BatchSize)
	vectors := make([][]float32, 0, len(texts))

	for start := 0; start < len(texts); start += batchSize {
		batch := texts[start:min(start+batchSize, len(texts))]

		embedded, err := c.embedBatch(ctx, batch)
		if err != nil {
			return nil, fmt.Errorf("failed to embed texts %d-%d: %w", start, start+len(batch)-1, err)
		}

		vectors = append(vectors, embedded...)
	}

	return vectors, nil
}

// effectiveBatchSize keeps EMBEDDINGS_BATCH_SIZE within what the API accepts.
func effectiveBatchSize(configured int) int {
	return min(max(configured, 1), maxBatchSize)
}

type embeddingsRequest struct {
	Model          string   `json:"model"`
	Input          []string `json:"input"`
	Dimensions     int      `json:"dimensions,omitempty"`
	EncodingFormat string   `json:"encoding_format"`
}

type embeddingsResponse struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

type apiErrorResponse struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
}

// statusError is a non-2xx response; retryable ones are retried by embedBatch.
type statusError struct {
	status     int
	code       string
	message    string
	retryAfter time.Duration
}

func (e *statusError) Error() string {
	return fmt.Sprintf("status %d: %s", e.status, e.message)
}

func (e *statusError) retryable() bool {
	if e.status == http.StatusTooManyRequests {
		return e.code != insufficientQuotaCode
	}

	return e.status >= http.StatusInternalServerError
}

func (c *Client) embedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	request := embeddingsRequest{Model: c.cfg.Model, Input: texts, EncodingFormat: encodingFloat}
	if strings.HasPrefix(c.cfg.Model, dimensionsModelPrefix) {
		request.Dimensions = Dimensions
	}

	body, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal embeddings request: %w", err)
	}

	for attempt := 0; ; attempt++ {
		vectors, err := c.post(ctx, body, len(texts))
		if err == nil {
			return vectors, nil
		}

		var se *statusError

		retryable := errors.As(err, &se) && se.retryable() || errors.Is(err, errTransport) && ctx.Err() == nil
		if !retryable || attempt >= c.cfg.MaxRetries {
			return nil, err
		}

		delay := c.backoff(attempt, se)
		c.log.WarnContext(ctx, "embeddings: retrying request", "error", err, "attempt", attempt+1, "delay", delay)

		if err := sleep(ctx, delay); err != nil {
			return nil, err
		}
	}
}

func (c *Client) post(ctx context.Context, body []byte, count int) ([][]float32, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+embeddingsPath, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create embeddings request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send embeddings request: %w: %w", errTransport, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: %w", ErrAPI, newStatusError(resp))
	}

	var parsed embeddingsResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("failed to decode embeddings response: %w: %w", ErrAPI, err)
	}

	return orderVectors(parsed, count)
}

// orderVectors puts vectors in input order and checks that every input got a vector of the right size.
func orderVectors(resp embeddingsResponse, count int) ([][]float32, error) {
	if len(resp.Data) != count {
		return nil, fmt.Errorf("got %d vectors for %d texts: %w", len(resp.Data), count, ErrAPI)
	}

	vectors := make([][]float32, count)
	for _, d := range resp.Data {
		if d.Index < 0 || d.Index >= count || vectors[d.Index] != nil {
			return nil, fmt.Errorf("unexpected vector index %d: %w", d.Index, ErrAPI)
		}

		if len(d.Embedding) != Dimensions {
			return nil, fmt.Errorf("got vector of size %d, want %d: %w", len(d.Embedding), Dimensions, ErrAPI)
		}

		vectors[d.Index] = d.Embedding
	}

	return vectors, nil
}

func newStatusError(resp *http.Response) *statusError {
	data, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodySize))

	message := strings.TrimSpace(string(data))

	se := &statusError{status: resp.StatusCode, message: message, retryAfter: retryAfter(resp.Header)}

	var apiErr apiErrorResponse
	if err := json.Unmarshal(data, &apiErr); err == nil && apiErr.Error.Message != "" {
		se.message = apiErr.Error.Message
		se.code = cmp.Or(apiErr.Error.Code, apiErr.Error.Type)
	}

	return se
}

// retryAfter reads the server's hint: OpenAI sends retry-after-ms, standard servers Retry-After in seconds.
func retryAfter(h http.Header) time.Duration {
	if ms, err := strconv.ParseFloat(h.Get("Retry-After-Ms"), float64Bits); err == nil && ms > 0 {
		return time.Duration(ms * float64(time.Millisecond))
	}

	if seconds, err := strconv.Atoi(h.Get("Retry-After")); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}

	return 0
}

// backoff doubles the base delay with each attempt, preferring the server's Retry-After.
func (c *Client) backoff(attempt int, se *statusError) time.Duration {
	if se != nil && se.retryAfter > 0 {
		return min(se.retryAfter, maxBackoff)
	}

	delay := c.cfg.RetryBaseDelay
	for range attempt {
		if delay >= maxBackoff {
			break
		}

		delay *= 2
	}

	return min(delay, maxBackoff)
}

func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return fmt.Errorf("failed to wait before retry: %w", ctx.Err())
	case <-timer.C:
		return nil
	}
}
