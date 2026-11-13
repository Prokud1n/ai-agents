package health

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"go.uber.org/fx"

	"github.com/Prokud1n/ai-agents/internal/providers/postgres"
	"github.com/Prokud1n/ai-agents/internal/providers/redis"
	"github.com/Prokud1n/ai-agents/internal/providers/s3"
)

const checkTimeout = 3 * time.Second

const (
	postgresDependency = "postgres"
	redisDependency    = "redis"
	s3Dependency       = "s3"
)

type pinger interface {
	Ping(ctx context.Context) error
}

type dependency struct {
	name   string
	pinger pinger
}

type HandlerParams struct {
	fx.In

	Log      *slog.Logger
	Postgres *postgres.Client
	Redis    *redis.Client
	S3       *s3.Client
}

type Handler struct {
	log          *slog.Logger
	dependencies []dependency
}

func NewHandler(p HandlerParams) *Handler {
	return &Handler{
		log: p.Log,
		dependencies: []dependency{
			{name: postgresDependency, pinger: p.Postgres},
			{name: redisDependency, pinger: p.Redis},
			{name: s3Dependency, pinger: p.S3},
		},
	}
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
	ctx, cancel := context.WithTimeout(r.Context(), checkTimeout)
	defer cancel()

	report := buildReport(h.check(ctx))
	if report.Status != OKStatus {
		h.log.WarnContext(ctx, "health: service is not ready", "dependencies", report.Dependencies)
	}

	h.writeJSON(ctx, w, report.HTTPStatus(), report)
}

func (h *Handler) check(ctx context.Context) []checkResult {
	results := make([]checkResult, len(h.dependencies))

	var wg sync.WaitGroup
	for i, d := range h.dependencies {
		wg.Go(func() {
			results[i] = checkResult{name: d.name, err: d.pinger.Ping(ctx)}
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
