package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"

	"go.uber.org/fx"

	"github.com/Prokud1n/ai-agents/internal/config"
)

// NewMux returns the router shared by all HTTP handlers; packages register their routes on it.
func NewMux() *http.ServeMux {
	return http.NewServeMux()
}

// NewServer starts listening on application start, so a busy port stops the startup.
func NewServer(lc fx.Lifecycle, cfg *config.HTTP, mux *http.ServeMux, log *slog.Logger) *http.Server {
	srv := &http.Server{
		Addr:              cfg.Address,
		Handler:           mux,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
	}

	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			var listenConfig net.ListenConfig

			listener, err := listenConfig.Listen(ctx, "tcp", cfg.Address)
			if err != nil {
				return fmt.Errorf("failed to listen on %s: %w", cfg.Address, err)
			}

			go func() {
				if err := srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
					log.Error("server: failed to serve http", "error", err)
				}
			}()

			log.InfoContext(ctx, "server: listening", "address", listener.Addr().String())

			return nil
		},
		OnStop: func(ctx context.Context) error {
			if err := srv.Shutdown(ctx); err != nil {
				return fmt.Errorf("failed to shut down http server: %w", err)
			}

			return nil
		},
	})

	return srv
}
