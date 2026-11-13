package server

import (
	"net/http"

	"go.uber.org/fx"
)

var Module = fx.Module("server",
	fx.Provide(NewMux, NewServer),
	fx.Invoke(func(*http.Server) {}),
)
