package health

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"go.uber.org/fx"
)

// checkTimeout is below the 3s timeout of the container healthcheck probe (cmd/healthcheck).
const checkTimeout = 2 * time.Second

// Checker is a dependency pinged by /readyz.
type Checker interface {
	Name() string
	Ping(ctx context.Context) error
}

type HandlerParams struct {
	fx.In

	Log      *slog.Logger
	Checkers []Checker `group:"readiness"`
}

type Handler struct {
	log      *slog.Logger
	checkers []Checker
	timeout  time.Duration
}

func NewHandler(p HandlerParams) *Handler {
	return &Handler{log: p.Log, checkers: p.Checkers, timeout: checkTimeout}
}

func RegisterRoutes(mux *http.ServeMux, h *Handler) {
	mux.HandleFunc("GET /healthz", h.Liveness)
	mux.HandleFunc("GET /readyz", h.Readiness)
}

// Liveness reports that the process is up; it does not touch dependencies.
func (h *Handler) Liveness(w http.ResponseWriter, r *http.Request) {
	h.writeJSON(r.Context(), w, http.StatusOK, map[string]Status{"status": OKStatus})
}

// Readiness pings every dependency concurrently and reports each one's status.
func (h *Handler) Readiness(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()

	results := h.check(ctx)
	for _, res := range results {
		if res.err != nil {
			h.log.WarnContext(ctx, "health: dependency is not ready", "dependency", res.name, "error", res.err)
		}
	}

	report := buildReport(results)
	h.writeJSON(ctx, w, report.HTTPStatus(), report)
}

func (h *Handler) check(ctx context.Context) []checkResult {
	results := make([]checkResult, len(h.checkers))

	var wg sync.WaitGroup
	for i, c := range h.checkers {
		wg.Go(func() {
			results[i] = checkResult{name: c.Name(), err: c.Ping(ctx)}
		})
	}

	wg.Wait()

	return results
}

func (h *Handler) writeJSON(ctx context.Context, w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)

	if err := json.NewEncoder(w).Encode(body); err != nil {
		h.log.ErrorContext(ctx, "health: failed to write response", "error", err)
	}
}
