package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-pdf/fpdf"

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
	hook, ok := h.service.GetHook(hookID)
	if !ok {
		http.NotFound(w, r)
		return
	}
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	switch format {
	case "", "jsonl", "ndjson":
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s.jsonl\"", hookID))
		if err := h.service.ExportRequests(hookID, w); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
	case "json", "pretty", "prettyjson", "pretty-json":
		if err := h.serveCaptureJSON(w, hook); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	case "pdf":
		if err := h.serveCapturePDF(w, hook); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	default:
		http.Error(w, "unsupported format (use jsonl, json, or pdf)", http.StatusBadRequest)
	}
}

func (h *captureHandlers) serveCaptureJSON(w http.ResponseWriter, hook capture.Hook) error {
	requests, err := h.loadHookRequests(hook.ID)
	if err != nil {
		return err
	}
	sort.SliceStable(requests, func(i, j int) bool {
		return requests[i].CreatedAt.Before(requests[j].CreatedAt)
	})
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s.json\"", hook.ID))
	payload := struct {
		Hook         capture.Hook          `json:"hook"`
		ExportedAt   time.Time             `json:"exported_at"`
		RequestCount int                   `json:"request_count"`
		Requests     []capture.HookRequest `json:"requests"`
	}{
		Hook:         hook,
		ExportedAt:   time.Now().UTC(),
		RequestCount: len(requests),
		Requests:     requests,
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(payload)
}

func (h *captureHandlers) serveCapturePDF(w http.ResponseWriter, hook capture.Hook) error {
	requests, err := h.loadHookRequests(hook.ID)
	if err != nil {
		return err
	}
	sort.SliceStable(requests, func(i, j int) bool {
		return requests[i].CreatedAt.Before(requests[j].CreatedAt)
	})
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=\"%s.pdf\"", hook.ID))
	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.SetMargins(12, 16, 12)
	tr := pdf.UnicodeTranslatorFromDescriptor("")
	brand := "LinkPeek"
	pdf.SetHeaderFuncMode(func() {
		pdf.SetFont("Helvetica", "B", 12)
		pdf.CellFormat(0, 8, tr(brand), "", 1, "C", false, 0, "")
		pdf.SetY(20)
	}, true)
	pdf.SetFooterFunc(func() {
		pdf.SetY(-12)
		pdf.SetFont("Helvetica", "", 9)
		pdf.CellFormat(0, 6, tr(fmt.Sprintf("Page %d", pdf.PageNo())), "T", 0, "R", false, 0, "")
	})
	pdf.AddPage()
	label := strings.TrimSpace(hook.Label)
	if label == "" {
		label = hook.ID
	}
	exported := time.Now().UTC()
	pdf.SetFont("Helvetica", "B", 14)
	pdf.CellFormat(0, 8, tr("Capture Log"), "", 1, "L", false, 0, "")
	pdf.SetFont("Helvetica", "", 11)
	pdf.CellFormat(0, 6, tr(fmt.Sprintf("Label: %s", label)), "", 1, "L", false, 0, "")
	pdf.CellFormat(0, 6, tr(fmt.Sprintf("Hook ID: %s", hook.ID)), "", 1, "L", false, 0, "")
	pdf.CellFormat(0, 6, tr(fmt.Sprintf("Exported: %s", exported.Format(time.RFC3339))), "", 1, "L", false, 0, "")
	pdf.CellFormat(0, 6, tr(fmt.Sprintf("Requests: %d", len(requests))), "", 1, "L", false, 0, "")
	pdf.Ln(2)
	if len(requests) == 0 {
		pdf.SetFont("Helvetica", "", 10)
		pdf.MultiCell(0, 5, tr("No captured requests found."), "", "L", false)
		return pdf.Output(w)
	}
	for _, req := range requests {
		ts := req.CreatedAt
		if ts.IsZero() {
			ts = exported
		}
		pdf.SetFont("Helvetica", "B", 11)
		pdf.CellFormat(0, 6, tr(ts.Format("2006-01-02 15:04:05 UTC")), "", 1, "L", false, 0, "")
		path := req.Path
		if req.Query != "" {
			path += "?" + req.Query
		}
		pdf.SetFont("Helvetica", "", 10)
		pdf.MultiCell(0, 5, tr(fmt.Sprintf("%s %s", strings.ToUpper(req.Method), path)), "", "L", false)
		remoteIP := req.RemoteIP
		if strings.TrimSpace(remoteIP) == "" {
			remoteIP = "—"
		}
		meta := fmt.Sprintf("Remote IP: %s    Body: %s", remoteIP, formatCaptureByteSize(req.BodySize))
		if req.BodyEncoding != "" {
			meta += fmt.Sprintf("    Encoding: %s", req.BodyEncoding)
		}
		if req.BodyTruncated {
			meta += "    (preview truncated)"
		}
		pdf.SetFont("Helvetica", "", 9)
		pdf.MultiCell(0, 5, tr(meta), "", "L", false)
		if len(req.Headers) > 0 {
			pdf.SetFont("Helvetica", "I", 9)
			pdf.CellFormat(0, 5, tr("Headers"), "", 1, "L", false, 0, "")
			keys := make([]string, 0, len(req.Headers))
			for k := range req.Headers {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			pdf.SetFont("Courier", "", 8)
			for _, k := range keys {
				line := fmt.Sprintf("%s: %s", k, req.Headers[k])
				pdf.MultiCell(0, 4, tr(line), "", "L", false)
			}
		}
		if strings.TrimSpace(req.BodyPreview) != "" {
			pdf.SetFont("Helvetica", "I", 9)
			pdf.CellFormat(0, 5, tr("Body Preview"), "", 1, "L", false, 0, "")
			pdf.SetFont("Courier", "", 8)
			for _, line := range strings.Split(req.BodyPreview, "\n") {
				pdf.MultiCell(0, 4, tr(line), "", "L", false)
			}
		}
		pdf.Ln(2)
	}
	return pdf.Output(w)
}

func (h *captureHandlers) loadHookRequests(hookID string) ([]capture.HookRequest, error) {
	var buf bytes.Buffer
	if err := h.service.ExportRequests(hookID, &buf); err != nil {
		return nil, err
	}
	if buf.Len() == 0 {
		return []capture.HookRequest{}, nil
	}
	scanner := bufio.NewScanner(bytes.NewReader(buf.Bytes()))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	requests := make([]capture.HookRequest, 0, 128)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var req capture.HookRequest
		if err := json.Unmarshal(line, &req); err != nil {
			continue
		}
		requests = append(requests, req)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return requests, nil
}

func formatCaptureByteSize(size int64) string {
	if size <= 0 {
		return "0 B"
	}
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	value := float64(size)
	exp := 0
	labels := []string{"KiB", "MiB", "GiB", "TiB", "PiB", "EiB"}
	for value >= 1024 && exp < len(labels)-1 {
		value /= 1024
		exp++
	}
	return fmt.Sprintf("%.1f %s", value, labels[exp])
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
