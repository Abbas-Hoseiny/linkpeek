package server

import (
	"net/http"
	"strings"

	"linkpeek/handlers"
	ap_runtime "linkpeek/internal/app/runtime"
	"linkpeek/middleware"
)

// registerRoutes wires all HTTP endpoints onto the provided mux.
func registerRoutes(rt *ap_runtime.Runtime, mux *http.ServeMux) {
	healthHandler := handlers.NewHealthHandler(rt.DB, rt.DataDir)
	authHandlers := newAuthHandlers(rt.AuthService)
	captureHandlers := newCaptureHandlers(rt.CaptureService)
	snippetHandlers := handlers.NewSnippetHandlers(rt.SnippetManager)
	scannerHandlers := handlers.NewScannerHandlers(rt.ScannerService)
	allowTunnelAdmin := false
	if rt != nil && rt.Config != nil {
		allowTunnelAdmin = rt.Config.AllowTunnelAdmin
	}
	tunnelHandlers := handlers.NewTunnelHandlers(rt.TunnelService, rt.RealtimeHub, allowTunnelAdmin)
	retryHandlers := handlers.NewRetryLabHandlers(rt.RetryLab, retryLabHeader)

	if rt.LoginLimiter != nil {
		mux.Handle("/login", middleware.WithRateLimit(rt.LoginLimiter)(http.HandlerFunc(authHandlers.login)))
	} else {
		mux.HandleFunc("/login", authHandlers.login)
	}

	mux.HandleFunc("/logout", authHandlers.logout)
	mux.HandleFunc("/api/auth/status", authHandlers.authStatus)
	mux.HandleFunc("/api/auth/change", authHandlers.authChange)
	mux.HandleFunc("/access", authHandlers.accessPage)

	mux.Handle("/healthz", healthHandler)
	mux.HandleFunc("/api/db/ping", handleDBPing)
	mux.HandleFunc("/api/echo", handleEcho)
	mux.HandleFunc("/api/events", handleEvents)
	mux.HandleFunc("/api/events/stream", handleEventsStream)
	mux.HandleFunc("/api/realtime", handleRealtime)
	mux.Handle("/api/tunnel", middleware.WithAllowedMethods(http.MethodGet)(http.HandlerFunc(tunnelHandlers.Tunnel)))
	mux.Handle("/api/tunnel/status", middleware.WithAllowedMethods(http.MethodGet)(http.HandlerFunc(tunnelHandlers.Status)))
	mux.Handle("/api/tunnel/restart", middleware.WithAllowedMethods(http.MethodPost)(http.HandlerFunc(tunnelHandlers.Restart)))
	mux.Handle("/api/tunnel/history", middleware.WithAllowedMethods(http.MethodGet)(http.HandlerFunc(tunnelHandlers.History)))
	mux.Handle("/api/tunnel/history/clear", middleware.WithAllowedMethods(http.MethodPost)(http.HandlerFunc(tunnelHandlers.ClearHistory)))

	payloadHandlers := handlers.NewPayloadHandlers(rt.PayloadService)
	mux.Handle("/api/payloads", middleware.WithAllowedMethods("GET", "POST")(http.HandlerFunc(payloadHandlers.Payloads)))
	mux.Handle("/api/payloads/", middleware.WithAllowedMethods("DELETE")(http.HandlerFunc(payloadHandlers.PayloadItem)))
	if snippetHandlers != nil {
		mux.Handle("/api/snippets", middleware.WithAllowedMethods("POST")(http.HandlerFunc(snippetHandlers.Create)))
		mux.HandleFunc("/snippet/raw/", snippetHandlers.Raw)
		mux.HandleFunc("/snippet/html/", snippetHandlers.HTML)
		mux.HandleFunc("/snippet/og/", snippetHandlers.OG)
	} else {
		mux.HandleFunc("/snippet/raw/", http.NotFound)
		mux.HandleFunc("/snippet/html/", http.NotFound)
		mux.HandleFunc("/snippet/og/", http.NotFound)
	}
	mux.Handle("/api/hooks", middleware.WithAllowedMethods("GET", "POST")(http.HandlerFunc(captureHandlers.hooks)))
	mux.HandleFunc("/api/hooks/", captureHandlers.hookRoutes)
	mux.HandleFunc("/api/hooks/activity", captureHandlers.captureActivity)

	mux.Handle("/api/scanner/jobs", middleware.WithAllowedMethods("GET", "POST")(http.HandlerFunc(scannerHandlers.Jobs)))
	mux.Handle("/api/scanner/jobs/", middleware.WithAllowedMethods("DELETE")(http.HandlerFunc(scannerHandlers.JobItem)))
	mux.HandleFunc("/api/scanner/results", scannerHandlers.Results)

	mux.Handle("/api/retrylab/scenarios", middleware.WithAllowedMethods(http.MethodGet)(http.HandlerFunc(retryHandlers.Scenarios)))
	mux.Handle("/api/retrylab/stats", middleware.WithAllowedMethods(http.MethodGet)(http.HandlerFunc(retryHandlers.Stats)))
	mux.Handle("/api/retrylab/reset", middleware.WithAllowedMethods(http.MethodPost)(http.HandlerFunc(retryHandlers.Reset)))
	mux.Handle("/retrylab/retry-hint", middleware.WithAllowedMethods(http.MethodGet)(http.HandlerFunc(retryHandlers.RetryHint)))
	mux.Handle("/retrylab/drop-after-n", middleware.WithAllowedMethods(http.MethodGet)(http.HandlerFunc(retryHandlers.DropAfterN)))
	mux.Handle("/retrylab/wrong-length", middleware.WithAllowedMethods(http.MethodGet)(http.HandlerFunc(retryHandlers.WrongLength)))

	mux.HandleFunc("/api/ip/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/summary"):
			handleIPSummary(w, r)
		case strings.HasSuffix(r.URL.Path, "/requests"):
			handleIPRequests(w, r)
		case strings.HasSuffix(r.URL.Path, "/geo"):
			handleIPGeo(w, r)
		case strings.HasSuffix(r.URL.Path, "/sizes"):
			handleIPSizes(w, r)
		case strings.HasSuffix(r.URL.Path, "/export"):
			handleIPExport(w, r)
		case strings.HasSuffix(r.URL.Path, "/delete"):
			handleIPDelete(w, r)
		default:
			http.NotFound(w, r)
		}
	})

	mux.HandleFunc("/api/ips", handleIPs)
	mux.HandleFunc("/api/ips/count", handleIPCount)
	mux.HandleFunc("/api/metrics", handleMetrics)

	mux.HandleFunc("/payload/raw/", payloadHandlers.Variant("raw"))
	mux.HandleFunc("/payload/inline/", payloadHandlers.Variant("inline"))
	mux.HandleFunc("/payload/download/", payloadHandlers.Variant("download"))
	mux.HandleFunc("/payload/mime-mismatch/", payloadHandlers.Variant("mime-mismatch"))
	mux.HandleFunc("/payload/corrupt/", payloadHandlers.Variant("corrupt"))
	mux.HandleFunc("/payload/slow/", payloadHandlers.Variant("slow"))
	mux.HandleFunc("/payload/chunked/", payloadHandlers.Variant("chunked"))
	mux.HandleFunc("/payload/redirect/", payloadHandlers.Variant("redirect"))
	mux.HandleFunc("/payload/error/", payloadHandlers.Variant("error"))
	mux.HandleFunc("/payload/range/", payloadHandlers.Variant("range"))
	mux.HandleFunc("/payload/spec/html/", payloadHandlers.SpectrumHTML)
	mux.HandleFunc("/payload/spec/og/", payloadHandlers.SpectrumOG)
	mux.HandleFunc("/payload/spec/", payloadHandlers.Spectrum)
	mux.HandleFunc("/payload/spec-html/", payloadHandlers.SpectrumHTML)
	mux.HandleFunc("/payload/spec-og/", payloadHandlers.SpectrumOG)

	mux.HandleFunc("/capture/", captureHandlers.captureRequest)

	mux.HandleFunc("/alpha", handleAlpha)
	mux.HandleFunc("/alpha/pixel", handleAlphaPixel)
	mux.HandleFunc("/alpha/js", handleAlphaJS)
	mux.HandleFunc("/alpha/challenge.js", handleAlphaChallengeJS)

	serveStatic(mux)
}
