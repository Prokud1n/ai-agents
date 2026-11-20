// Package redistest gives integration tests a real Redis.
//
// Redis of the compose stack is not published to the host, so `make test-integration` starts a separate
// throwaway Redis container (no volume, published on 127.0.0.1:REDIS_TEST_HOST_PORT, 6380 by default) and
// passes its address in REDIS_TEST_ADDRESS. The rules are the same as in pgtest: a test with "Integration"
// in its name calls Connect, which skips it unless INTEGRATION=1; with INTEGRATION=1 a missing
// REDIS_TEST_ADDRESS fails the test instead of skipping it.
//
// Isolation: tests share one Redis, so each takes its own key prefix (Prefix) and the prefix's keys are
// deleted when the test ends.
package redistest

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/fx/fxtest"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/providers/redis"
)

const (
	enableEnv   = "INTEGRATION"
	addressEnv  = "REDIS_TEST_ADDRESS"
	passwordEnv = "REDIS_TEST_PASSWORD"
	scanCount   = 100
)

var prefixCounter atomic.Int64

// Connect returns a started client of the test Redis; it is closed when the test ends.
// Without INTEGRATION=1 the test is skipped.
func Connect(t *testing.T) *redis.Client {
	t.Helper()

	if os.Getenv(enableEnv) != "1" {
		t.Skip("integration test: run with INTEGRATION=1 and a test Redis (make up && make test-integration)")
	}

	address := os.Getenv(addressEnv)
	require.NotEmpty(t, address, "%s is not set: run the tests with make test-integration", addressEnv)

	lc := fxtest.NewLifecycle(t)
	client := redis.NewClient(lc, &config.Redis{Address: address, Password: os.Getenv(passwordEnv)},
		slog.New(slog.DiscardHandler))

	lc.RequireStart()
	t.Cleanup(lc.RequireStop)

	return client
}

// Prefix returns a key prefix unique to the test; keys starting with it are deleted when the test ends.
func Prefix(t *testing.T, client *redis.Client) string {
	t.Helper()

	prefix := fmt.Sprintf("test:%d:%d:%d:", time.Now().UnixNano(), os.Getpid(), prefixCounter.Add(1))

	t.Cleanup(func() {
		ctx := context.Background()
		iter := client.Conn().Scan(ctx, 0, prefix+"*", scanCount).Iterator()

		for iter.Next(ctx) {
			if err := client.Conn().Del(ctx, iter.Val()).Err(); err != nil {
				t.Errorf("failed to delete test key %s: %v", iter.Val(), err)
			}
		}

		if err := iter.Err(); err != nil {
			t.Errorf("failed to scan test keys: %v", err)
		}
	})

	return prefix
}
