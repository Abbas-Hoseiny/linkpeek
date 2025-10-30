package router

import (
	"errors"
	"net/http"

	appRuntime "linkpeek/internal/app/runtime"
)

// Middleware represents a standard HTTP middleware wrapper.
type Middleware func(http.Handler) http.Handler

// Builder registers routes onto the provided mux.
type Builder func(*http.ServeMux) error

// Config captures the inputs required to construct the HTTP handler.
type Config struct {
	Runtime     *appRuntime.Runtime
	BuildRoutes Builder
	Middlewares []Middleware
}

// New constructs a fully-initialised HTTP handler using the supplied configuration.
func New(cfg Config) (http.Handler, error) {
	if cfg.Runtime == nil {
		return nil, errors.New("router: runtime is required")
	}

	mux := http.NewServeMux()
	if cfg.BuildRoutes != nil {
		if err := cfg.BuildRoutes(mux); err != nil {
			return nil, err
		}
	}

	var handler http.Handler = mux
	if len(cfg.Middlewares) > 0 {
		for i := len(cfg.Middlewares) - 1; i >= 0; i-- {
			handler = cfg.Middlewares[i](handler)
		}
	}

	return handler, nil
}
