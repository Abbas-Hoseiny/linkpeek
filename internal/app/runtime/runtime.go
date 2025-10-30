package runtime

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"net/http"
	"time"

	"linkpeek/internal/auth"
	"linkpeek/internal/config"
	domainauth "linkpeek/internal/domain/auth"
	domaincapture "linkpeek/internal/domain/capture"
	domainpayload "linkpeek/internal/domain/payload"
	domainretry "linkpeek/internal/domain/retry"
	domainscanner "linkpeek/internal/domain/scanner"
	domainsnippet "linkpeek/internal/domain/snippet"
	domaintunnel "linkpeek/internal/domain/tunnel"
	"linkpeek/internal/realtime"
	"linkpeek/middleware"
)

// SessionStore encapsulates the session manager behaviour required by the runtime.
type SessionStore interface {
	Create() (string, time.Time)
	Validate(string) (bool, time.Time)
	Delete(string)
	Close()
}

// Logger represents the subset of the standard logger we rely on.
type Logger interface {
	Printf(string, ...interface{})
}

// Dependencies aggregates the collaborators required to build the application runtime.
type Dependencies struct {
	Config          *config.Config
	Logger          Logger
	DataDir         string
	PayloadDir      string
	DB              *sql.DB
	AuthStore       *auth.Store
	Sessions        SessionStore
	AuthService     *domainauth.Service
	CaptureService  *domaincapture.Service
	PayloadService  *domainpayload.Service
	RetryLab        *domainretry.Lab
	ScannerService  *domainscanner.Service
	SnippetManager  *domainsnippet.Manager
	TunnelService   *domaintunnel.Service
	LoginLimiter    *middleware.RateLimiter
	HTTPClient      *http.Client
	RealtimeHub     *realtime.Hub
	ShutdownTimeout time.Duration
}

// Runtime provides access to shared application collaborators.
type Runtime struct {
	Config          *config.Config
	Logger          Logger
	DataDir         string
	PayloadDir      string
	DB              *sql.DB
	AuthStore       *auth.Store
	Sessions        SessionStore
	AuthService     *domainauth.Service
	CaptureService  *domaincapture.Service
	PayloadService  *domainpayload.Service
	RetryLab        *domainretry.Lab
	ScannerService  *domainscanner.Service
	SnippetManager  *domainsnippet.Manager
	TunnelService   *domaintunnel.Service
	LoginLimiter    *middleware.RateLimiter
	HTTPClient      *http.Client
	RealtimeHub     *realtime.Hub
	shutdownTimeout time.Duration
}

// New constructs a runtime from the provided dependencies without mutating behaviour.
func New(deps Dependencies) (*Runtime, error) {
	if deps.Config == nil {
		return nil, errors.New("runtime: config is required")
	}
	if deps.Logger == nil {
		deps.Logger = log.Default()
	}
	rt := &Runtime{
		Config:          deps.Config,
		Logger:          deps.Logger,
		DataDir:         deps.DataDir,
		PayloadDir:      deps.PayloadDir,
		DB:              deps.DB,
		AuthStore:       deps.AuthStore,
		Sessions:        deps.Sessions,
		AuthService:     deps.AuthService,
		CaptureService:  deps.CaptureService,
		PayloadService:  deps.PayloadService,
		RetryLab:        deps.RetryLab,
		ScannerService:  deps.ScannerService,
		SnippetManager:  deps.SnippetManager,
		TunnelService:   deps.TunnelService,
		LoginLimiter:    deps.LoginLimiter,
		HTTPClient:      deps.HTTPClient,
		RealtimeHub:     deps.RealtimeHub,
		shutdownTimeout: deps.ShutdownTimeout,
	}
	if rt.shutdownTimeout <= 0 {
		rt.shutdownTimeout = 10 * time.Second
	}
	return rt, nil
}

// Shutdown performs graceful teardown of collaborators that support it.
func (rt *Runtime) Shutdown(ctx context.Context) {
	if rt.RealtimeHub != nil {
		rt.RealtimeHub.Stop()
		rt.Logger.Printf("realtime: hub stopped")
	}
	if rt.Sessions != nil {
		rt.Sessions.Close()
	}
	if rt.DB != nil {
		if err := rt.DB.Close(); err != nil {
			rt.Logger.Printf("database close error: %v", err)
		}
	}
}

// ShutdownTimeout returns the maximum duration allowed for graceful shutdown.
func (rt *Runtime) ShutdownTimeout() time.Duration {
	return rt.shutdownTimeout
}
