package server

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"

	"github.com/Prokud1n/ai-agents/internal/config"
)

const (
	testTimeout   = 5 * time.Second
	listeningMsg  = "server: listening"
	testRoutePath = "/ping"
)

// syncBuffer collects log output; the server logs from its own goroutine.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}

// listenAddress finds the actual address (port :0 is chosen by the OS) in the "server: listening" record.
func listenAddress(t *testing.T, logs string) string {
	t.Helper()

	for line := range bytes.Lines([]byte(logs)) {
		var record struct {
			Msg     string `json:"msg"`
			Address string `json:"address"`
		}

		require.NoError(t, json.Unmarshal(line, &record))

		if record.Msg == listeningMsg {
			return record.Address
		}
	}

	require.Failf(t, "no listening record in logs", "logs: %s", logs)

	return ""
}

func get(t *testing.T, url string) (int, error) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, http.NoBody)
	require.NoError(t, err)

	// No keep-alive: every request opens a new connection, so a stopped server is really not reachable.
	client := &http.Client{Timeout: testTimeout, Transport: &http.Transport{DisableKeepAlives: true}}

	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	return resp.StatusCode, nil
}

func TestNewServerStartsAndStops(t *testing.T) {
	t.Parallel()

	logs := &syncBuffer{}

	app := fxtest.New(t,
		fx.Supply(
			&config.HTTP{
				Address:           "127.0.0.1:0",
				ReadHeaderTimeout: testTimeout,
				ReadTimeout:       testTimeout,
				WriteTimeout:      testTimeout,
				IdleTimeout:       testTimeout,
			},
			slog.New(slog.NewJSONHandler(logs, nil)),
		),
		fx.Provide(NewMux, NewServer),
		fx.Invoke(func(mux *http.ServeMux, _ *http.Server) {
			mux.HandleFunc("GET "+testRoutePath, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			})
		}),
	)

	app.RequireStart()

	url := "http://" + listenAddress(t, logs.String()) + testRoutePath

	status, err := get(t, url)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNoContent, status)

	app.RequireStop()

	_, err = get(t, url)
	require.Error(t, err, "server must not accept connections after stop")
}
