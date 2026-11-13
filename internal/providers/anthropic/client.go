package anthropic

import (
	"bytes"
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

const (
	messagesPath     = "/v1/messages"
	apiVersion       = "2023-06-01"
	authHeader       = "x-api-key"
	versionHeader    = "anthropic-version"
	requestIDHeader  = "request-id"
	retryAfterHeader = "retry-after"
	maxErrorBodySize = 64 << 10
	maxBackoffShift  = 20 // caps 2^attempt so the delay never overflows
)

// Client is a thin client for the Claude Messages API: one endpoint, retries on 429/5xx.
type Client struct {
	httpClient     *http.Client
	baseURL        string
	apiKey         string
	maxRetries     int
	retryBaseDelay time.Duration
	retryMaxDelay  time.Duration
	log            *slog.Logger
}

func NewClient(cfg *config.Anthropic, log *slog.Logger) *Client {
	return &Client{
		httpClient:     &http.Client{Timeout: cfg.Timeout},
		baseURL:        strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:         cfg.APIKey,
		maxRetries:     max(cfg.MaxRetries, 0),
		retryBaseDelay: cfg.RetryBaseDelay,
		retryMaxDelay:  cfg.RetryMaxDelay,
		log:            log,
	}
}

// Configured reports whether the API key is set.
func (c *Client) Configured() bool {
	return c.apiKey != ""
}

// CreateMessage sends a Messages API request. 429, 5xx and network errors are retried
// up to MaxRetries times with exponential backoff (or the server's retry-after, if longer).
func (c *Client) CreateMessage(ctx context.Context, req *MessageRequest) (*MessageResponse, error) {
	if !c.Configured() {
		return nil, ErrAPIKeyMissing
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to encode message request: %w", err)
	}

	for attempt := 0; ; attempt++ {
		resp, retryAfter, err := c.send(ctx, body)
		if err == nil {
			return resp, nil
		}

		if attempt >= c.maxRetries || !retryable(ctx, err) {
			return nil, fmt.Errorf("failed to create message: %w", err)
		}

		delay := backoff(c.retryBaseDelay, c.retryMaxDelay, attempt, retryAfter)
		c.log.WarnContext(ctx, "anthropic: retrying request",
			"error", err, "attempt", attempt+1, "delay", delay.String())

		if err := sleep(ctx, delay); err != nil {
			return nil, fmt.Errorf("failed to create message: %w", err)
		}
	}
}

// send makes one attempt. It returns the server's retry-after delay (zero if absent) for retries.
func (c *Client) send(ctx context.Context, body []byte) (*MessageResponse, time.Duration, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+messagesPath, bytes.NewReader(body))
	if err != nil {
		return nil, 0, fmt.Errorf("failed to create http request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set(authHeader, c.apiKey)
	httpReq.Header.Set(versionHeader, apiVersion)

	httpResp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, 0, &transportError{err: err}
	}
	defer httpResp.Body.Close()

	requestID := httpResp.Header.Get(requestIDHeader)

	if httpResp.StatusCode != http.StatusOK {
		return nil, parseRetryAfter(httpResp.Header.Get(retryAfterHeader)), decodeAPIError(httpResp, requestID)
	}

	var resp MessageResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, 0, fmt.Errorf("failed to decode message response: %w", err)
	}

	resp.RequestID = requestID

	return &resp, 0, nil
}

func decodeAPIError(httpResp *http.Response, requestID string) *APIError {
	apiErr := &APIError{StatusCode: httpResp.StatusCode, RequestID: requestID}

	data, err := io.ReadAll(io.LimitReader(httpResp.Body, maxErrorBodySize))
	if err != nil {
		apiErr.Message = "failed to read error body: " + err.Error()

		return apiErr
	}

	var body errorBody
	if err := json.Unmarshal(data, &body); err != nil || body.Error.Message == "" {
		apiErr.Message = strings.TrimSpace(string(data))

		return apiErr
	}

	apiErr.Type = body.Error.Type
	apiErr.Message = body.Error.Message

	return apiErr
}

// transportError is a failure to get any response; such requests are retried.
type transportError struct {
	err error
}

func (e *transportError) Error() string {
	return "failed to send http request: " + e.err.Error()
}

func (e *transportError) Unwrap() error {
	return e.err
}

// retryable: API errors with 429/5xx and network errors, but never a cancelled or expired context.
// A broken 200 response is not retried: the request is already billed.
func retryable(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false
	}

	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Retryable()
	}

	var transportErr *transportError

	return errors.As(err, &transportErr)
}

// backoff returns base * 2^attempt capped by maxDelay; a longer retry-after from the server wins,
// but is capped by maxDelay too, so one call never waits unbounded.
func backoff(base, maxDelay time.Duration, attempt int, retryAfter time.Duration) time.Duration {
	delay := maxDelay
	if shift := min(attempt, maxBackoffShift); base <= maxDelay>>shift {
		delay = base << shift
	}

	return max(delay, min(retryAfter, maxDelay))
}

// parseRetryAfter reads the delay in whole seconds; the HTTP-date form is not used by the API.
func parseRetryAfter(value string) time.Duration {
	seconds, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || seconds <= 0 {
		return 0
	}

	return time.Duration(seconds) * time.Second
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
