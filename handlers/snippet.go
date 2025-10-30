package handlers

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	neturl "net/url"
	"strconv"
	"strings"

	domainsnippet "linkpeek/internal/domain/snippet"
	"linkpeek/internal/utils"
)

// SnippetHandlers serves snippet creation and retrieval endpoints.
type SnippetHandlers struct {
	manager *domainsnippet.Manager
}

// NewSnippetHandlers constructs snippet handlers around the provided manager.
func NewSnippetHandlers(manager *domainsnippet.Manager) *SnippetHandlers {
	if manager == nil {
		return nil
	}
	return &SnippetHandlers{manager: manager}
}

// Create handles POST /api/snippets requests.
func (h *SnippetHandlers) Create(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.manager == nil {
		http.Error(w, "snippet storage unavailable", http.StatusServiceUnavailable)
		return
	}

	type snippetRequest struct {
		Content  string `json:"content"`
		MIME     string `json:"mime"`
		Filename string `json:"filename"`
	}

	limit := h.manager.MaxBytes()
	if limit <= 0 {
		limit = 64 * 1024
	}

	var req snippetRequest
	dec := json.NewDecoder(io.LimitReader(r.Body, int64(limit)+4096))
	if err := dec.Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	content := []byte(req.Content)
	if len(content) == 0 {
		http.Error(w, "content required", http.StatusBadRequest)
		return
	}

	entry, err := h.manager.Create(content, req.MIME, req.Filename)
	if err != nil {
		if strings.Contains(err.Error(), "too large") {
			http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id":       entry.ID,
		"mime":     entry.MIME,
		"filename": entry.Filename,
		"size":     entry.Size,
		"urls": map[string]string{
			"raw":  fmt.Sprintf("/snippet/raw/%s", entry.ID),
			"html": fmt.Sprintf("/snippet/html/%s", entry.ID),
			"og":   fmt.Sprintf("/snippet/og/%s", entry.ID),
		},
	})
}

// Raw serves GET/HEAD requests for /snippet/raw/{id}.
func (h *SnippetHandlers) Raw(w http.ResponseWriter, r *http.Request) {
	h.serveSnippet(w, r, "raw", []string{"/snippet/raw/"})
}

// HTML serves GET/HEAD requests for /snippet/html/{id}.
func (h *SnippetHandlers) HTML(w http.ResponseWriter, r *http.Request) {
	h.serveSnippet(w, r, "html", []string{"/snippet/html/"})
}

// OG serves GET/HEAD requests for /snippet/og/{id}.
func (h *SnippetHandlers) OG(w http.ResponseWriter, r *http.Request) {
	h.serveSnippet(w, r, "og", []string{"/snippet/og/"})
}

func (h *SnippetHandlers) serveSnippet(w http.ResponseWriter, r *http.Request, mode string, prefixes []string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h == nil || h.manager == nil {
		http.NotFound(w, r)
		return
	}
	var (
		id string
		ok bool
	)
	for _, prefix := range prefixes {
		if id, ok = domainsnippet.ExtractID(r.URL.Path, prefix); ok {
			break
		}
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	entry, found := h.manager.Get(id)
	if !found {
		http.NotFound(w, r)
		return
	}
	if entry.ETag != "" && etagMatches(entry.ETag, r.Header.Get("If-None-Match")) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	filename := entry.Filename
	if filename == "" {
		filename = entry.ID + ".txt"
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Snippet-Id", entry.ID)
	if entry.ETag != "" {
		w.Header().Set("ETag", entry.ETag)
	}
	switch mode {
	case "raw":
		mimeType := entry.MIME
		if mimeType == "" {
			mimeType = "text/plain; charset=utf-8"
		}
		w.Header().Set("Content-Type", mimeType)
		w.Header().Set("Content-Disposition", utils.ContentDisposition("inline", filename))
		w.Header().Set("Content-Length", strconv.Itoa(entry.Size))
		if r.Method == http.MethodHead || entry.Size == 0 {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(entry.Content)
	case "html":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Disposition", utils.ContentDisposition("inline", filename))
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		escaped := html.EscapeString(string(entry.Content))
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "<!doctype html><html lang=\"en\"><head><meta charset=\"utf-8\"><title>%s</title><meta name=\"viewport\" content=\"width=device-width,initial-scale=1\"></head><body style=\"margin:0;background:#0f172a;color:#e2e8f0;font-family:ui-monospace,Menlo,Monaco,Consolas,'Liberation Mono','Courier New',monospace;display:flex;justify-content:center;align-items:center;min-height:100vh;padding:24px;\"><pre style=\"white-space:pre-wrap;word-break:break-word;background:rgba(15,23,42,0.8);padding:24px;border-radius:12px;box-shadow:0 18px 40px rgba(15,23,42,0.45);max-width:100%%;width:100%%;overflow:auto;\">%s</pre></body></html>", html.EscapeString(filename), escaped)
	case "og":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Disposition", utils.ContentDisposition("inline", filename))
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		rawURL := fmt.Sprintf("/snippet/raw/%s", neturl.PathEscape(entry.ID))
		desc := string(entry.Content)
		if len(desc) > 180 {
			desc = desc[:180]
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "<!doctype html><html lang=\"en\"><head><meta charset=\"utf-8\"><title>%s</title><meta property=\"og:title\" content=\"%s\"><meta property=\"og:description\" content=\"%s\"><meta property=\"og:type\" content=\"article\"><meta property=\"og:url\" content=\"%s\"></head><body></body></html>", html.EscapeString(filename), html.EscapeString(filename), html.EscapeString(desc), html.EscapeString(rawURL))
	default:
		http.NotFound(w, r)
	}
}
