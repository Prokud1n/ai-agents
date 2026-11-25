package editjob

import (
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/fx/fxtest"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/providers/redis"
)

// TestNewBoardJobLifetime: a job that may wait and run as long as half of its record's lifetime is refused at the
// start, so a current job never expires on the board; a shorter one is accepted.
func TestNewBoardJobLifetime(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		queue       time.Duration
		job         time.Duration
		expectedErr error
	}{
		{name: "defaults", queue: 3 * time.Hour, job: 60 * time.Minute},
		{name: "just under the limit", queue: 83 * time.Hour, job: time.Hour - time.Second},
		{name: "at the limit", queue: 83 * time.Hour, job: time.Hour, expectedErr: ErrInvalidConfig},
		{name: "over the limit", queue: 7 * 24 * time.Hour, job: time.Hour, expectedErr: ErrInvalidConfig},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// The client is never started: NewBoard only keeps it.
			client := redis.NewClient(fxtest.NewLifecycle(t), &config.Redis{Address: "127.0.0.1:1"},
				slog.New(slog.DiscardHandler))

			board, err := NewBoard(client, &config.Editor{
				Stream: "editor:jobs", JobTimeout: tt.job, PollInterval: time.Minute, QueueTimeout: tt.queue,
				WorkerWait: time.Minute,
			}, slog.New(slog.DiscardHandler))

			if tt.expectedErr != nil {
				require.ErrorIs(t, err, tt.expectedErr)
				assert.Nil(t, board)

				return
			}

			require.NoError(t, err)
			assert.NotNil(t, board)
		})
	}
}
