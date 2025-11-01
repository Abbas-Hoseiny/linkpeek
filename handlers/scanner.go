package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	fpdf "github.com/go-pdf/fpdf"

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

// ExportResults streams scanner results in multiple formats for download.
func (h *ScannerHandlers) ExportResults(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.service == nil {
		http.Error(w, "scanner service unavailable", http.StatusServiceUnavailable)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	switch format {
	case "", "json", "pretty", "pretty-json", "prettyjson":
		format = "json"
	case "jsonl", "ndjson":
		format = "jsonl"
	case "pdf":
		// ok
	default:
		http.Error(w, "unsupported format (use json, jsonl, or pdf)", http.StatusBadRequest)
		return
	}
	limit := clampInt(parseIntDefault(r.URL.Query().Get("limit"), 200), 1, 2000)
	jobID := strings.TrimSpace(r.URL.Query().Get("job"))
	results := h.service.Results(limit, jobID)
	generatedAt := time.Now().UTC()
	baseName := "scanner"
	if jobID != "" {
		baseName += "-" + jobID
	}
	w.Header().Set("Cache-Control", "no-store")
	var filename string
	switch format {
	case "jsonl":
		filename = makeScannerExportFilename(baseName, "jsonl", generatedAt)
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))
		if err := writeScannerResultsNDJSON(w, results); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	case "pdf":
		filename = makeScannerExportFilename(baseName, "pdf", generatedAt)
		w.Header().Set("Content-Type", "application/pdf")
		w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=\"%s\"", filename))
		if err := writeScannerResultsPDF(w, results, jobID, generatedAt); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	default:
		filename = makeScannerExportFilename(baseName, "json", generatedAt)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))
		if err := writeScannerResultsPrettyJSON(w, results, jobID, generatedAt); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
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

func writeScannerResultsNDJSON(w io.Writer, results []domainscanner.Result) error {
	enc := json.NewEncoder(w)
	for _, res := range results {
		if err := enc.Encode(res); err != nil {
			return err
		}
	}
	return nil
}

func writeScannerResultsPrettyJSON(w io.Writer, results []domainscanner.Result, jobID string, generatedAt time.Time) error {
	payload := struct {
		JobID       string                 `json:"job_id,omitempty"`
		GeneratedAt time.Time              `json:"generated_at"`
		Count       int                    `json:"count"`
		Items       []domainscanner.Result `json:"items"`
	}{
		JobID:       jobID,
		GeneratedAt: generatedAt,
		Count:       len(results),
		Items:       results,
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(payload)
}

func writeScannerResultsPDF(w io.Writer, results []domainscanner.Result, jobID string, generatedAt time.Time) error {
	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.SetMargins(12, 16, 12)
	tr := pdf.UnicodeTranslatorFromDescriptor("")
	pdf.SetHeaderFuncMode(func() {
		pdf.SetFont("Helvetica", "B", 12)
		pdf.CellFormat(0, 8, tr("LinkPeek"), "", 1, "C", false, 0, "")
		pdf.SetY(20)
	}, true)
	pdf.SetFooterFunc(func() {
		pdf.SetY(-12)
		pdf.SetFont("Helvetica", "", 9)
		pdf.CellFormat(0, 6, tr(fmt.Sprintf("Page %d", pdf.PageNo())), "T", 0, "R", false, 0, "")
	})
	pdf.AddPage()
	pdf.SetFont("Helvetica", "B", 14)
	pdf.CellFormat(0, 8, tr("Scanner Results"), "", 1, "L", false, 0, "")
	pdf.SetFont("Helvetica", "", 11)
	if jobID != "" {
		pdf.CellFormat(0, 6, tr(fmt.Sprintf("Job filter: %s", jobID)), "", 1, "L", false, 0, "")
	} else {
		pdf.CellFormat(0, 6, tr("Job filter: all"), "", 1, "L", false, 0, "")
	}
	pdf.CellFormat(0, 6, tr(fmt.Sprintf("Generated: %s", generatedAt.Format(time.RFC3339))), "", 1, "L", false, 0, "")
	pdf.CellFormat(0, 6, tr(fmt.Sprintf("Results: %d", len(results))), "", 1, "L", false, 0, "")
	pdf.Ln(2)
	if len(results) == 0 {
		pdf.SetFont("Helvetica", "", 10)
		pdf.MultiCell(0, 5, tr("No scanner executions recorded for the selected filters."), "", "L", false)
		return pdf.Output(w)
	}
	for _, res := range results {
		ts := res.Timestamp
		if ts.IsZero() {
			ts = generatedAt
		}
		pdf.SetFont("Helvetica", "B", 11)
		pdf.CellFormat(0, 6, tr(ts.Format("2006-01-02 15:04:05 UTC")), "", 1, "L", false, 0, "")
		titleParts := []string{}
		if res.JobName != "" {
			titleParts = append(titleParts, res.JobName)
		}
		if res.JobID != "" {
			titleParts = append(titleParts, fmt.Sprintf("(%s)", res.JobID))
		}
		if len(titleParts) > 0 {
			pdf.SetFont("Helvetica", "", 10)
			pdf.MultiCell(0, 5, tr(strings.Join(titleParts, " ")), "", "L", false)
		}
		verb := strings.ToUpper(strings.TrimSpace(res.Method))
		if verb == "" {
			verb = "GET"
		}
		pdf.SetFont("Helvetica", "", 10)
		pdf.MultiCell(0, 5, tr(fmt.Sprintf("%s %s", verb, strings.TrimSpace(res.URL))), "", "L", false)
		meta := []string{}
		if res.Status > 0 {
			meta = append(meta, fmt.Sprintf("Status: %d", res.Status))
		}
		if res.DurationMs > 0 {
			meta = append(meta, fmt.Sprintf("Duration: %d ms", res.DurationMs))
		}
		if res.Error != "" {
			meta = append(meta, "Error: "+res.Error)
		}
		if len(meta) > 0 {
			pdf.SetFont("Helvetica", "", 9)
			pdf.MultiCell(0, 5, tr(strings.Join(meta, "    ")), "", "L", false)
		}
		if strings.TrimSpace(res.ResponseSnippet) != "" {
			pdf.SetFont("Helvetica", "I", 9)
			pdf.CellFormat(0, 5, tr("Response snippet"), "", 1, "L", false, 0, "")
			pdf.SetFont("Courier", "", 8)
			for _, line := range strings.Split(res.ResponseSnippet, "\n") {
				pdf.MultiCell(0, 4, tr(line), "", "L", false)
			}
		}
		pdf.Ln(2)
	}
	return pdf.Output(w)
}

func makeScannerExportFilename(base, ext string, generatedAt time.Time) string {
	safe := sanitizeScannerFilenameComponent(base)
	if safe == "" {
		safe = "scanner"
	}
	stamp := generatedAt.UTC().Format("20060102-150405")
	return fmt.Sprintf("%s-%s.%s", safe, stamp, ext)
}

func sanitizeScannerFilenameComponent(s string) string {
	if s == "" {
		return ""
	}
	var builder strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			builder.WriteRune(r)
		case r == '-', r == '_':
			builder.WriteRune(r)
		default:
			builder.WriteRune('-')
		}
	}
	return strings.Trim(builder.String(), "-_")
}
