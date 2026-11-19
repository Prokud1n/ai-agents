package anthropic

import (
	"errors"
	"fmt"
	"net/http"
)

// ErrAPIKeyMissing is returned by every call when ANTHROPIC_API_KEY is not set.
var ErrAPIKeyMissing = errors.New("anthropic api key is not set: set ANTHROPIC_API_KEY")

// APIError is a non-2xx response of the Claude API.
type APIError struct {
	StatusCode int
	Type       string // error.type from the body, e.g. "rate_limit_error"
	Message    string
	RequestID  string
	// ShouldRetry is the x-should-retry response header; nil when the header is absent.
	ShouldRetry *bool
}

func (e *APIError) Error() string {
	return fmt.Sprintf("anthropic api error: status %d, type %q, request_id %q: %s",
		e.StatusCode, e.Type, e.RequestID, e.Message)
}

// Retryable reports whether the request may succeed when repeated. The server's x-should-retry
// header wins; without it rate limits and server errors (including 529 overloaded) are retried.
func (e *APIError) Retryable() bool {
	if e.ShouldRetry != nil {
		return *e.ShouldRetry
	}

	return e.StatusCode == http.StatusTooManyRequests || e.StatusCode >= http.StatusInternalServerError
}

type errorBody struct {
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}
