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

const serveFailureExitCode = 1

type Params struct {
	fx.In

	Lifecycle  fx.Lifecycle
	Shutdowner fx.Shutdowner
	Config     *config.HTTP
	Mux        *http.ServeMux
	Log        *slog.Logger
}

// NewMux returns the router shared by all HTTP handlers; packages register their routes on it.
func NewMux() *http.ServeMux {
	return http.NewServeMux()
}

// NewServer starts listening on application start, so a busy port stops the startup.
// If serving fails later, the whole application shuts down with a non-zero exit code.
func NewServer(p Params) *http.Server {
	srv := &http.Server{
		Addr:              p.Config.Address,
		Handler:           p.Mux,
		ReadHeaderTimeout: p.Config.ReadHeaderTimeout,
		ReadTimeout:       p.Config.ReadTimeout,
		WriteTimeout:      p.Config.WriteTimeout,
		IdleTimeout:       p.Config.IdleTimeout,
	}

	p.Lifecycle.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			var listenConfig net.ListenConfig

			listener, err := listenConfig.Listen(ctx, "tcp", p.Config.Address)
			if err != nil {
				return fmt.Errorf("failed to listen on %s: %w", p.Config.Address, err)
			}

			serveCtx := context.WithoutCancel(ctx)

			go func() {
				if err := srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
					p.Log.ErrorContext(serveCtx, "server: failed to serve http", "error", err)

					if err := p.Shutdowner.Shutdown(fx.ExitCode(serveFailureExitCode)); err != nil {
						p.Log.ErrorContext(serveCtx, "server: failed to shut down application", "error", err)
					}
				}
			}()

			p.Log.InfoContext(ctx, "server: listening", "address", listener.Addr().String())

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
