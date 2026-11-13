package health

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testTimeout = 50 * time.Millisecond

var errSecret = errors.New("dial tcp 10.0.0.5:5432: connection refused")

type fakeChecker struct {
	name string
	err  error
	hang bool
}

func (f fakeChecker) Name() string {
	return f.name
}

func (f fakeChecker) Ping(ctx context.Context) error {
	if f.hang {
		<-ctx.Done()

		return ctx.Err()
	}

	return f.err
}

func newTestHandler(checkers ...Checker) *Handler {
	return &Handler{
		log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		checkers: checkers,
		timeout:  testTimeout,
	}
}

func TestHandlerCheck(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    Checker
		expected error
	}{
		{name: "success", input: fakeChecker{name: postgresName}},
		{name: "error", input: fakeChecker{name: redisName, err: errSecret}, expected: errSecret},
		{name: "hang ends with timeout", input: fakeChecker{name: s3Name, hang: true}, expected: context.DeadlineExceeded},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithTimeout(t.Context(), testTimeout)
			defer cancel()

			started := time.Now()
			got := newTestHandler(tt.input).check(ctx)

			require.Len(t, got, 1)
			assert.Equal(t, tt.input.Name(), got[0].name)
			assert.Less(t, time.Since(started), time.Second)

			if tt.expected == nil {
				require.NoError(t, got[0].err)

				return
			}

			require.ErrorIs(t, got[0].err, tt.expected)
		})
	}
}

func TestHandlerReadiness(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		input        []Checker
		expectedCode int
		expectedBody string
	}{
		{
			name:         "ready",
			input:        []Checker{fakeChecker{name: postgresName}, fakeChecker{name: redisName}},
			expectedCode: http.StatusOK,
			expectedBody: `{"status":"ok","dependencies":{"postgres":"ok","redis":"ok"}}`,
		},
		{
			name: "error text is not exposed",
			input: []Checker{
				fakeChecker{name: postgresName, err: errSecret},
				fakeChecker{name: s3Name, hang: true},
			},
			expectedCode: http.StatusServiceUnavailable,
			expectedBody: `{"status":"fail","dependencies":{"postgres":"fail","s3":"fail"}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", http.NoBody)
			rec := httptest.NewRecorder()

			newTestHandler(tt.input...).Readiness(rec, req)

			assert.Equal(t, tt.expectedCode, rec.Code)
			assert.JSONEq(t, tt.expectedBody, rec.Body.String())
			assert.NotContains(t, rec.Body.String(), "10.0.0.5")
		})
	}
}
