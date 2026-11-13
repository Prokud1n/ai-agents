package whisper

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Prokud1n/ai-agents/internal/config"
)

const (
	transcriptionsPath = "/audio/transcriptions"

	responseFormat     = "verbose_json"
	granularitiesField = "timestamp_granularities[]"
	wordGranularity    = "word"
	segmentGranularity = "segment"

	// MaxFileSize is the upload limit of /audio/transcriptions (25 MB).
	MaxFileSize = 25 << 20

	maxResponseSize = 32 << 20 // verbose_json of a 25 MB file is far smaller
	maxErrorSize    = 64 << 10
	maxRetryDelay   = time.Minute

	// insufficient_quota also comes with 429, but waiting does not help.
	insufficientQuotaCode = "insufficient_quota"
)

var (
	// ErrAPIKeyMissing is returned on a call when OPENAI_API_KEY is not set: the application still starts.
	ErrAPIKeyMissing = errors.New("openai api key is not configured (OPENAI_API_KEY)")
	ErrFileTooLarge  = errors.New("audio file exceeds the 25 MB upload limit")
)

// Client calls the OpenAI speech-to-text endpoint with word and segment timestamps.
type Client struct {
	httpClient     *http.Client
	url            string
	apiKey         string
	model          string
	language       string
	requestTimeout time.Duration
	maxRetries     int
	retryBaseDelay time.Duration
	log            *slog.Logger
}

func NewClient(openAI *config.OpenAI, cfg *config.Whisper, log *slog.Logger) *Client {
	return &Client{
		httpClient:     &http.Client{},
		url:            strings.TrimRight(openAI.BaseURL, "/") + transcriptionsPath,
		apiKey:         openAI.APIKey,
		model:          cfg.Model,
		language:       cfg.Language,
		requestTimeout: cfg.RequestTimeout,
		maxRetries:     max(cfg.MaxRetries, 0),
		retryBaseDelay: cfg.RetryBaseDelay,
		log:            log,
	}
}

// Transcribe uploads one audio file and returns its transcript with word and segment timestamps.
// Requests failing with 429 (except insufficient quota), 5xx or a network error are retried
// with exponential backoff; Retry-After is honored.
func (c *Client) Transcribe(ctx context.Context, data TranscribeData) (*Transcript, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("failed to transcribe: %w", ErrAPIKeyMissing)
	}

	body, contentType, err := c.buildBody(data)
	if err != nil {
		return nil, err
	}

	for attempt := 0; ; attempt++ {
		transcript, err := c.send(ctx, body, contentType)
		if err == nil {
			return transcript, nil
		}

		if attempt >= c.maxRetries || !isRetryable(ctx, err) {
			return nil, fmt.Errorf("failed to transcribe %s after %d attempt(s): %w", filepath.Base(data.Path), attempt+1, err)
		}

		delay := retryDelay(attempt, c.retryBaseDelay, retryAfter(err))
		c.log.WarnContext(ctx, "whisper: retrying transcription", "attempt", attempt+1, "delay", delay, "error", err)

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()

			return nil, fmt.Errorf("failed to transcribe: %w", ctx.Err())
		case <-timer.C:
		}
	}
}

// buildBody reads the file once into a multipart body, so every retry re-sends the same bytes.
func (c *Client) buildBody(data TranscribeData) (body []byte, contentType string, err error) {
	audio, err := os.ReadFile(filepath.Clean(data.Path))
	if err != nil {
		return nil, "", fmt.Errorf("failed to read audio file: %w", err)
	}

	if len(audio) > MaxFileSize {
		return nil, "", fmt.Errorf("failed to upload %s (%d bytes): %w", filepath.Base(data.Path), len(audio), ErrFileTooLarge)
	}

	language := data.Language
	if language == "" {
		language = c.language
	}

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	fields := [][2]string{
		{"model", c.model},
		{"response_format", responseFormat},
		{granularitiesField, wordGranularity},
		{granularitiesField, segmentGranularity},
		{"language", language},
		{"prompt", data.Prompt},
	}

	for _, f := range fields {
		if f[1] == "" {
			continue
		}

		if err := w.WriteField(f[0], f[1]); err != nil {
			return nil, "", fmt.Errorf("failed to write form field %s: %w", f[0], err)
		}
	}

	// The API detects the audio format by the file name extension.
	part, err := w.CreateFormFile("file", filepath.Base(data.Path))
	if err != nil {
		return nil, "", fmt.Errorf("failed to create form file: %w", err)
	}

	if _, err := part.Write(audio); err != nil {
		return nil, "", fmt.Errorf("failed to write form file: %w", err)
	}

	if err := w.Close(); err != nil {
		return nil, "", fmt.Errorf("failed to close multipart body: %w", err)
	}

	return buf.Bytes(), w.FormDataContentType(), nil
}

func (c *Client) send(ctx context.Context, body []byte, contentType string) (*Transcript, error) {
	ctx, cancel := context.WithTimeout(ctx, c.requestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", contentType)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, newStatusError(resp)
	}

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	return parseTranscript(respBody)
}

// StatusError is a non-200 response of the API.
type StatusError struct {
	StatusCode int
	Code       string // OpenAI error code, e.g. "insufficient_quota"
	Message    string
	RetryAfter time.Duration
}

func (e *StatusError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("openai status %d (%s): %s", e.StatusCode, e.Code, e.Message)
	}

	return fmt.Sprintf("openai status %d: %s", e.StatusCode, e.Message)
}

func newStatusError(resp *http.Response) *StatusError {
	// Best effort: the status code alone is enough to decide on a retry.
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorSize))

	var payload struct {
		Error struct {
			Message string `json:"message"`
			Code    any    `json:"code"`
		} `json:"error"`
	}

	e := &StatusError{StatusCode: resp.StatusCode, RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"))}

	if json.Unmarshal(raw, &payload) == nil && payload.Error.Message != "" {
		e.Message = payload.Error.Message
		if code, ok := payload.Error.Code.(string); ok {
			e.Code = code
		}
	} else {
		e.Message = strings.TrimSpace(string(raw))
	}

	return e
}

func isRetryable(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false
	}

	var se *StatusError
	if errors.As(err, &se) {
		if se.StatusCode == http.StatusTooManyRequests {
			return se.Code != insufficientQuotaCode
		}

		return se.StatusCode >= http.StatusInternalServerError
	}

	// Network errors and the per-request timeout: the parent context is still alive.
	return true
}

func retryAfter(err error) time.Duration {
	var se *StatusError
	if errors.As(err, &se) {
		return se.RetryAfter
	}

	return 0
}

// retryDelay is base·2^attempt capped at maxRetryDelay, but not less than the server's Retry-After.
func retryDelay(attempt int, base, after time.Duration) time.Duration {
	delay := base
	for range attempt {
		delay *= 2
		if delay >= maxRetryDelay {
			delay = maxRetryDelay

			break
		}
	}

	return min(max(delay, after), maxRetryDelay)
}

// parseRetryAfter reads Retry-After in seconds; the HTTP-date form is not used by OpenAI.
func parseRetryAfter(value string) time.Duration {
	seconds, err := strconv.ParseFloat(strings.TrimSpace(value), float64Bits)
	if err != nil || seconds <= 0 {
		return 0
	}

	return time.Duration(seconds * float64(time.Second))
}

const float64Bits = 64
