package telegram

import (
	"errors"
	"fmt"
	"net/http"
	"time"
)

// ErrInvalidToken: the token is not in the "<bot id>:<secret>" form.
var ErrInvalidToken = errors.New("invalid telegram bot token")

// APIError is a response with "ok": false (or a non-JSON error response of a proxy).
type APIError struct {
	Code        int
	Description string
	// RetryAfter is parameters.retry_after of a 429 response.
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	return fmt.Sprintf("telegram bot api error %d: %s", e.Code, e.Description)
}

// Retryable: flood control (429) and server errors; 4xx means the request itself is wrong.
func (e *APIError) Retryable() bool {
	return e.Code == http.StatusTooManyRequests || e.Code >= http.StatusInternalServerError
}

// transportError is a failure to get any response. Its message never contains the request URL with the
// token: the URL is masked before the error is created.
type transportError struct {
	err error
}

func (e *transportError) Error() string {
	return "failed to send http request: " + e.err.Error()
}

func (e *transportError) Unwrap() error {
	return e.err
}
