package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"linkpeek/internal/capture"
	domaincapture "linkpeek/internal/domain/capture"
	"linkpeek/internal/utils"
)

// captureHandlers orchestrates capture endpoints using the domain service.
type captureHandlers struct {
	service *domaincapture.Service
}

func newCaptureHandlers(service *domaincapture.Service) *captureHandlers {
	return &captureHandlers{service: service}
}

func (h *captureHandlers) ensureAvailable(w http.ResponseWriter) bool {
	if h == nil || !h.service.Available() {
		http.Error(w, "capture disabled", http.StatusServiceUnavailable)
		return false
	}
	return true
}

func (h *captureHandlers) hooks(w http.ResponseWriter, r *http.Request) {
	if !h.ensureAvailable(w) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		items := h.service.ListHooks()
		respondJSON(w, map[string]any{"items": items})
	case http.MethodPost:
		var payload struct {
			Label string `json:"label"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		hook, err := h.service.CreateHook(payload.Label)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		respondJSON(w, hook)
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *captureHandlers) hookRoutes(w http.ResponseWriter, r *http.Request) {
	if !h.ensureAvailable(w) {
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/hooks/")
	if path == "" || path == r.URL.Path {
		http.NotFound(w, r)
		return
	}
	parts := strings.Split(path, "/")
	hookID := parts[0]
	if hookID == "" {
		http.NotFound(w, r)
		return
	}
	switch {
	case len(parts) == 1 && r.Method == http.MethodDelete:
		if err := h.service.DeleteHook(hookID); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		respondJSON(w, map[string]any{"ok": true})
	case len(parts) == 2 && parts[1] == "clear" && r.Method == http.MethodPost:
		if err := h.service.ClearRequests(hookID); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		respondJSON(w, map[string]any{"ok": true})
	case len(parts) == 2 && parts[1] == "requests" && r.Method == http.MethodGet:
		h.hookRequests(w, r, hookID)
	case len(parts) == 2 && parts[1] == "download" && r.Method == http.MethodGet:
		h.hookDownload(w, r, hookID)
	default:
		http.NotFound(w, r)
	}
}

func (h *captureHandlers) hookRequests(w http.ResponseWriter, r *http.Request, hookID string) {
	if _, ok := h.service.GetHook(hookID); !ok {
		http.NotFound(w, r)
		return
	}
	limit := clamp(parseIntDefault(r.URL.Query().Get("limit"), 100), 1, 500)
	items := h.service.ListRequests(hookID, limit)
	respondJSON(w, map[string]any{"items": items})
}

func (h *captureHandlers) hookDownload(w http.ResponseWriter, r *http.Request, hookID string) {
	if _, ok := h.service.GetHook(hookID); !ok {
		http.NotFound(w, r)
		return
	}
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	if format != "" && format != "jsonl" && format != "ndjson" {
		http.Error(w, "unsupported format (use jsonl)", http.StatusBadRequest)
		return
	}
	if r.URL.Query().Get("format") == "" {
		format = "jsonl"
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s.jsonl\"", hookID))
	if err := h.service.ExportRequests(hookID, w); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
}

func (h *captureHandlers) captureActivity(w http.ResponseWriter, r *http.Request) {
	if !h.ensureAvailable(w) {
		return
	}
	limit := clamp(parseIntDefault(r.URL.Query().Get("limit"), 50), 1, 500)
	items := h.service.RecentRequests(limit)
	respondJSON(w, map[string]any{"items": items})
}

func (h *captureHandlers) captureRequest(w http.ResponseWriter, r *http.Request) {
	if !h.ensureAvailable(w) {
		return
	}
	token := strings.TrimPrefix(r.URL.Path, "/capture/")
	token = strings.Trim(token, "/")
	if token == "" {
		http.NotFound(w, r)
		return
	}
	hook, ok := h.service.HookByToken(token)
	if !ok {
		http.NotFound(w, r)
		return
	}
	body, size, truncated, err := capture.ReadBody(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	preview, encoding, previewTrunc := capture.EncodeBodyPreview(body)
	headers := make(map[string]string, len(r.Header))
	for k, v := range r.Header {
		if len(v) == 0 {
			continue
		}
		headers[strings.ToLower(k)] = strings.Join(v, ", ")
	}
	req := capture.HookRequest{
		Method:        r.Method,
		Host:          r.Host,
		Path:          r.URL.Path,
		Query:         r.URL.RawQuery,
		Headers:       headers,
		RemoteIP:      utils.RemoteIP(r),
		UserAgent:     r.UserAgent(),
		BodyPreview:   preview,
		BodyEncoding:  encoding,
		BodySize:      size,
		BodyTruncated: truncated || previewTrunc,
	}
	recorded, err := h.service.RecordRequest(hook.ID, req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	markCaptureRequest(r, hook.ID)

	w.Header().Set("Content-Type", "application/json")
	respondJSON(w, map[string]any{
		"ok":      true,
		"hook":    hook,
		"request": recorded,
	})
}
