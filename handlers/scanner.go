package handlers

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	domainscanner "linkpeek/internal/domain/scanner"
)

// ScannerHandlers exposes HTTP endpoints for managing scanner jobs and results.
type ScannerHandlers struct {
	service *domainscanner.Service
}

// NewScannerHandlers constructs scanner HTTP handlers.
func NewScannerHandlers(service *domainscanner.Service) *ScannerHandlers {
	return &ScannerHandlers{service: service}
}

// Jobs handles listing and creating scanner jobs.
func (h *ScannerHandlers) Jobs(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if h == nil || h.service == nil {
			http.Error(w, "scanner service unavailable", http.StatusServiceUnavailable)
			return
		}
		items := h.service.ListJobs()
		respondJSON(w, map[string]any{"items": items})
	case http.MethodPost:
		if h == nil || h.service == nil {
			http.Error(w, "scanner service unavailable", http.StatusServiceUnavailable)
			return
		}
		var req struct {
			Name            string `json:"name"`
			Method          string `json:"method"`
			URL             string `json:"url"`
			IntervalSeconds int    `json:"interval_seconds"`
			Body            string `json:"body"`
			ContentType     string `json:"content_type"`
			Active          *bool  `json:"active"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		active := true
		if req.Active != nil {
			active = *req.Active
		}
		job, err := h.service.CreateJob(domainscanner.CreateJobRequest{
			Name:            req.Name,
			Method:          req.Method,
			URL:             req.URL,
			IntervalSeconds: req.IntervalSeconds,
			Body:            req.Body,
			ContentType:     req.ContentType,
			Active:          active,
		})
		if err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, domainscanner.ErrServiceUnavailable) {
				status = http.StatusServiceUnavailable
			}
			http.Error(w, err.Error(), status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{"item": job})
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// JobItem handles deletion of individual jobs.
func (h *ScannerHandlers) JobItem(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.service == nil {
		http.Error(w, "scanner service unavailable", http.StatusServiceUnavailable)
		return
	}
	if r.Method != http.MethodDelete {
		w.Header().Set("Allow", http.MethodDelete)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id, ok := extractScannerID(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := h.service.DeleteJob(id); err != nil {
		if errors.Is(err, domainscanner.ErrJobNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Results serves scanner execution results and clears them when requested.
func (h *ScannerHandlers) Results(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.service == nil {
		http.Error(w, "scanner service unavailable", http.StatusServiceUnavailable)
		return
	}
	if r.Method == http.MethodDelete {
		if err := h.service.ClearResults(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		respondJSON(w, map[string]any{"message": "Scanner results cleared"})
		return
	}
	n := clampInt(parseIntDefault(r.URL.Query().Get("n"), 100), 1, 500)
	jobID := strings.TrimSpace(r.URL.Query().Get("job"))
	items := h.service.Results(n, jobID)
	respondJSON(w, map[string]any{"items": items})
}

func extractScannerID(path string) (string, bool) {
	const prefix = "/api/scanner/jobs/"
	if !strings.HasPrefix(path, prefix) {
		return "", false
	}
	id := strings.Trim(strings.TrimPrefix(path, prefix), "/")
	if id == "" {
		return "", false
	}
	return id, true
}

func parseIntDefault(s string, def int) int {
	if s == "" {
		return def
	}
	if v, err := strconv.Atoi(s); err == nil {
		return v
	}
	return def
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
