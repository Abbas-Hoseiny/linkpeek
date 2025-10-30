package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"

	domainretry "linkpeek/internal/domain/retry"
	"linkpeek/internal/utils"
)

// RetryLabHandlers exposes endpoints for retry lab scenarios and stats.
type RetryLabHandlers struct {
	lab       *domainretry.Lab
	headerKey string
}

// NewRetryLabHandlers constructs retry lab HTTP handlers.
func NewRetryLabHandlers(lab *domainretry.Lab, headerKey string) *RetryLabHandlers {
	return &RetryLabHandlers{lab: lab, headerKey: headerKey}
}

func (h *RetryLabHandlers) ensureLab(w http.ResponseWriter) bool {
	if h == nil || h.lab == nil {
		http.Error(w, "retry lab unavailable", http.StatusServiceUnavailable)
		return false
	}
	return true
}

// Scenarios lists the configured retry lab scenarios with absolute URLs.
func (h *RetryLabHandlers) Scenarios(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.ensureLab(w) {
		return
	}
	scenarios := h.lab.ListScenarios()
	items := make([]map[string]any, 0, len(scenarios))
	for _, sc := range scenarios {
		items = append(items, map[string]any{
			"id":          sc.ID,
			"title":       sc.Title,
			"description": sc.Description,
			"url":         h.scenarioURL(r, sc.Path),
		})
	}
	respondJSON(w, map[string]any{"items": items})
}

// Stats returns the current retry lab statistics snapshot.
func (h *RetryLabHandlers) Stats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.ensureLab(w) {
		return
	}
	stats := h.lab.SnapshotStats()
	respondJSON(w, map[string]any{"items": stats})
}

// Reset clears retry lab statistics. Optional scenario IDs can be provided via
// JSON body {"ids": ["retry-hint"]} or the query parameters id / ids.
func (h *RetryLabHandlers) Reset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.ensureLab(w) {
		return
	}

	type resetPayload struct {
		IDs []string `json:"ids"`
	}

	idSet := map[string]struct{}{}

	q := r.URL.Query()
	if id := strings.TrimSpace(q.Get("id")); id != "" {
		idSet[id] = struct{}{}
	}
	for _, id := range q["ids"] {
		if trimmed := strings.TrimSpace(id); trimmed != "" {
			idSet[trimmed] = struct{}{}
		}
	}

	if r.Body != nil && r.Body != http.NoBody {
		defer r.Body.Close()
		var payload resetPayload
		dec := json.NewDecoder(r.Body)
		if err := dec.Decode(&payload); err != nil && err != io.EOF {
			http.Error(w, "invalid json body", http.StatusBadRequest)
			return
		}
		for _, id := range payload.IDs {
			if trimmed := strings.TrimSpace(id); trimmed != "" {
				idSet[trimmed] = struct{}{}
			}
		}
	}

	var ids []string
	for id := range idSet {
		ids = append(ids, id)
	}

	h.lab.Reset(ids...)

	stats := h.lab.SnapshotStats()
	respondJSON(w, map[string]any{"items": stats})
}

// RetryHint implements the retry-hint scenario.
func (h *RetryLabHandlers) RetryHint(w http.ResponseWriter, r *http.Request) {
	if !h.ensureLab(w) {
		return
	}
	const id = "retry-hint"
	h.markRequest(r, id)
	ip := utils.RemoteIP(r)
	h.lab.RecordHit(id, ip)
	log.Printf("retrylab %s hit from %s", id, ip)

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Retry-After", "5")
	w.WriteHeader(http.StatusServiceUnavailable)
	respondJSON(w, map[string]any{
		"error":       "service_unavailable",
		"message":     "Upstream maintenance in progress. Please retry later.",
		"retry_after": 5,
	})
}

// DropAfterN implements the drop-after-n scenario.
func (h *RetryLabHandlers) DropAfterN(w http.ResponseWriter, r *http.Request) {
	if !h.ensureLab(w) {
		return
	}
	const id = "drop-after-n"
	h.markRequest(r, id)
	ip := utils.RemoteIP(r)
	h.lab.RecordHit(id, ip)
	log.Printf("retrylab %s hit from %s", id, ip)

	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)

	chunk := make([]byte, 256)
	for i := range chunk {
		chunk[i] = byte('A' + (i % 26))
	}
	if _, err := w.Write(chunk[:192]); err == nil {
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
	}
	if hj, ok := w.(http.Hijacker); ok {
		if conn, _, err := hj.Hijack(); err == nil {
			conn.Close()
			return
		}
	}
}

// WrongLength implements the wrong-length scenario.
func (h *RetryLabHandlers) WrongLength(w http.ResponseWriter, r *http.Request) {
	if !h.ensureLab(w) {
		return
	}
	const id = "wrong-length"
	h.markRequest(r, id)
	ip := utils.RemoteIP(r)
	h.lab.RecordHit(id, ip)
	log.Printf("retrylab %s hit from %s", id, ip)

	expected := 1024
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(expected))
	w.WriteHeader(http.StatusOK)
	body := "This payload advertises more data than it sends. Clients should detect truncation."
	_, _ = io.WriteString(w, body)
}

func (h *RetryLabHandlers) markRequest(r *http.Request, id string) {
	if r == nil || h.headerKey == "" {
		return
	}
	r.Header.Set(h.headerKey, id)
}

func (h *RetryLabHandlers) scenarioURL(r *http.Request, path string) string {
	if r == nil {
		return path
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	} else if proto := strings.ToLower(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto"))); proto != "" {
		scheme = proto
	}
	host := r.Host
	if host == "" {
		host = "localhost"
	}
	return fmt.Sprintf("%s://%s%s", scheme, host, path)
}
