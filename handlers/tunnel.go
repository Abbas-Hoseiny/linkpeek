package handlers

import (
	"errors"
	"net/http"
	"strings"

	domaintunnel "linkpeek/internal/domain/tunnel"
	"linkpeek/internal/realtime"
)

// TunnelHandlers exposes HTTP endpoints for managing tunnel status and history.
type TunnelHandlers struct {
	service          *domaintunnel.Service
	realtimeHub      *realtime.Hub
	allowTunnelAdmin bool
}

// NewTunnelHandlers constructs tunnel HTTP handlers.
func NewTunnelHandlers(service *domaintunnel.Service, hub *realtime.Hub, allowAdmin bool) *TunnelHandlers {
	return &TunnelHandlers{service: service, realtimeHub: hub, allowTunnelAdmin: allowAdmin}
}

func (h *TunnelHandlers) ensureService(w http.ResponseWriter) bool {
	if h == nil || h.service == nil {
		http.Error(w, "tunnel service unavailable", http.StatusServiceUnavailable)
		return false
	}
	return true
}

// Tunnel returns the most recently observed tunnel URL.
func (h *TunnelHandlers) Tunnel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.ensureService(w) {
		return
	}
	url := strings.TrimSpace(h.service.ReadLastURL())
	resp := map[string]string{"url": url}
	if url != "" {
		resp["alpha_url"] = strings.TrimRight(url, "/") + "/alpha"
	}
	respondJSON(w, resp)
}

// Status refreshes and returns the current tunnel status.
func (h *TunnelHandlers) Status(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.ensureService(w) {
		return
	}
	h.service.RefreshStatus()
	respondJSON(w, h.service.GetStatus())
}

// Restart attempts to restart the cloudflared container when configured.
func (h *TunnelHandlers) Restart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.ensureService(w) {
		return
	}
	if h.service.IsTunnelHost(r.Host) && !h.allowTunnelAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	err := h.service.RestartCloudflared(r.Context(), 3)
	switch {
	case err == nil:
		respondJSON(w, map[string]any{"ok": true})
		go h.service.RefreshStatus()
	case errors.Is(err, domaintunnel.ErrRestartNotConfigured):
		http.Error(w, "restart not configured", http.StatusNotImplemented)
	case errors.Is(err, domaintunnel.ErrDockerSocketUnavailable):
		http.Error(w, "docker socket unavailable", http.StatusServiceUnavailable)
	default:
		http.Error(w, err.Error(), http.StatusBadGateway)
	}
}

// History lists recently observed tunnel URLs.
func (h *TunnelHandlers) History(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.ensureService(w) {
		return
	}
	n := clampInt(parseIntDefault(r.URL.Query().Get("n"), 100), 1, 500)
	items := h.service.GetHistory()
	if len(items) > n {
		items = items[:n]
	}
	respondJSON(w, items)
}

// ClearHistory truncates the stored tunnel history.
func (h *TunnelHandlers) ClearHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.ensureService(w) {
		return
	}
	if h.service.IsTunnelHost(r.Host) && !h.allowTunnelAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := h.service.ClearHistory(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	respondJSON(w, map[string]any{"ok": true})
}
