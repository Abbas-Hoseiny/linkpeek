package handlers

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"math/rand"
	"mime"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"linkpeek/internal/apierror"
	domainpayload "linkpeek/internal/domain/payload"
	"linkpeek/internal/types"
	"linkpeek/internal/utils"
)

// PayloadHandlers orchestrates payload endpoints using the domain service.
type PayloadHandlers struct {
	service *domainpayload.Service
}

func NewPayloadHandlers(service *domainpayload.Service) *PayloadHandlers {
	return &PayloadHandlers{service: service}
}

func respondJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func etagMatches(etag, header string) bool {
	if etag == "" || header == "" {
		return false
	}
	for _, part := range strings.Split(header, ",") {
		if strings.TrimSpace(part) == etag {
			return true
		}
	}
	return false
}

func (h *PayloadHandlers) maxUploadBytes() int64 {
	if h == nil || h.service == nil {
		return 0
	}
	return h.service.MaxUploadBytes()
}

func (h *PayloadHandlers) Payloads(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		items := h.snapshotList()
		respondJSON(w, map[string]any{"items": items})
	case http.MethodPost:
		if h == nil || h.service == nil {
			http.Error(w, "payload storage unavailable", http.StatusServiceUnavailable)
			return
		}
		limit := h.maxUploadBytes()
		if limit > 0 {
			r.Body = http.MaxBytesReader(w, r.Body, limit)
		} else {
			limit = 32 << 20 // Default parsing limit when max is not configured
		}
		if err := r.ParseMultipartForm(limit); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			http.Error(w, "file field required", http.StatusBadRequest)
			return
		}
		defer file.Close()

		name := strings.TrimSpace(r.FormValue("name"))
		if name == "" {
			name = header.Filename
		}
		if name == "" {
			name = "Payload"
		}
		category := strings.TrimSpace(r.FormValue("category"))

		data, err := io.ReadAll(file)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		contentType := header.Header.Get("Content-Type")
		if contentType == "" && len(data) > 0 {
			sniff := len(data)
			if sniff > 512 {
				sniff = 512
			}
			contentType = http.DetectContentType(data[:sniff])
		}
		if contentType == "" && header.Filename != "" {
			if mt := mime.TypeByExtension(strings.ToLower(filepath.Ext(header.Filename))); mt != "" {
				contentType = mt
			}
		}
		if contentType == "" {
			contentType = "application/octet-stream"
		}

		meta, err := h.service.Create(data, header.Filename, name, category, contentType)
		if err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "exceeds") {
				http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		item := types.PayloadListItem{Payload: *meta, Variants: h.service.GetVariants(meta.ID)}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(item)
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *PayloadHandlers) PayloadItem(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.service == nil {
		http.Error(w, "payload storage unavailable", http.StatusServiceUnavailable)
		return
	}
	id, ok := domainpayload.ExtractIDFromPath(r.URL.Path, "/api/payloads/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := h.service.Delete(id); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "not found") {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *PayloadHandlers) Variant(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h == nil || h.service == nil {
			http.NotFound(w, r)
			return
		}
		id, ok := domainpayload.ExtractIDFromPath(r.URL.Path, "/payload/"+kind+"/")
		if !ok {
			http.NotFound(w, r)
			return
		}
		meta, ok := h.service.Get(id)
		if !ok {
			http.NotFound(w, r)
			return
		}
		switch kind {
		case "raw":
			h.servePayloadFile(w, r, meta, "", "")
		case "inline":
			h.servePayloadFile(w, r, meta, "", utils.ContentDisposition("inline", meta.OriginalFilename))
		case "download":
			h.servePayloadFile(w, r, meta, "", utils.ContentDisposition("attachment", meta.OriginalFilename))
		case "mime-mismatch":
			h.servePayloadFile(w, r, meta, mismatchMime(meta.MimeType), "")
		case "corrupt":
			h.servePayloadCorrupt(w, r, meta)
		case "slow":
			h.servePayloadSlow(w, r, meta, 500*time.Millisecond)
		case "chunked":
			h.servePayloadSlow(w, r, meta, 0)
		case "redirect":
			http.Redirect(w, r, fmt.Sprintf("/payload/inline/%s?via=redirect", meta.ID), http.StatusFound)
		case "error":
			h.servePayloadError(w, r, meta)
		case "range":
			h.servePayloadRange(w, r, meta)
		default:
			http.NotFound(w, r)
		}
	}
}

func (h *PayloadHandlers) Spectrum(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h == nil || h.service == nil {
		http.NotFound(w, r)
		return
	}
	id, seedStr, ok := extractSpectrumParams(r.URL.Path, "/payload/spec/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	meta, ok := h.service.Get(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	seed, flags, etag := deriveSpectrumSeed(meta.ID, seedStr)
	if etag != "" && etagMatches(etag, r.Header.Get("If-None-Match")) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	data, err := h.generateSpectrumVariant(meta, seed)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	queryMime := strings.TrimSpace(r.URL.Query().Get("mime"))
	profile := computeSpectrumHeaders(meta, queryMime, flags)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Spectrum-Seed", seedStr)
	if profile.MIME != "" {
		w.Header().Set("Content-Type", profile.MIME)
	}
	if profile.AcceptRanges {
		w.Header().Set("Accept-Ranges", "bytes")
	} else {
		w.Header().Set("Accept-Ranges", "none")
	}
	if etag != "" {
		w.Header().Set("ETag", etag)
	}
	if disp := profile.Disposition; disp != "" {
		filename := meta.OriginalFilename
		if disp == "attachment" || filename == "" {
			filename = spectrumFilename(meta, seedStr)
		}
		w.Header().Set("Content-Disposition", utils.ContentDisposition(disp, filename))
	}
	if len(data) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method == http.MethodHead || !profile.Chunked {
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	}
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	if profile.Chunked {
		writeChunked(w, data, 2048)
		return
	}
	_, _ = w.Write(data)
}

func (h *PayloadHandlers) SpectrumHTML(w http.ResponseWriter, r *http.Request) {
	h.serveSpectrumWrapper(w, r, "html", []string{"/payload/spec/html/", "/payload/spec-html/"})
}

func (h *PayloadHandlers) SpectrumOG(w http.ResponseWriter, r *http.Request) {
	h.serveSpectrumWrapper(w, r, "og", []string{"/payload/spec/og/", "/payload/spec-og/"})
}

func (h *PayloadHandlers) serveSpectrumWrapper(w http.ResponseWriter, r *http.Request, mode string, prefixes []string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h == nil || h.service == nil {
		http.NotFound(w, r)
		return
	}
	if len(prefixes) == 0 {
		prefixes = []string{"/payload/spec/"}
	}
	var (
		id      string
		seedStr string
		ok      bool
	)
	for _, prefix := range prefixes {
		if id, seedStr, ok = extractSpectrumParams(r.URL.Path, prefix); ok {
			break
		}
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	meta, ok := h.service.Get(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	_, flags, _ := deriveSpectrumSeed(meta.ID, seedStr)
	profile := computeSpectrumHeaders(meta, r.URL.Query().Get("mime"), flags)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Spectrum-Seed", seedStr)
	sanSeed := sanitizeToken(seedStr)
	if sanSeed == "" {
		sanSeed = fmt.Sprintf("s%d", time.Now().Unix()%1000)
	}
	w.Header().Set("ETag", fmt.Sprintf(`W/"spec-%s-%s-%s"`, mode, meta.ID, sanSeed))
	if profile.Disposition != "" {
		filename := meta.OriginalFilename
		if filename == "" {
			filename = spectrumFilename(meta, seedStr)
		}
		w.Header().Set("Content-Disposition", utils.ContentDisposition("inline", filename))
	}
	imagePath := fmt.Sprintf("/payload/spec/%s/%s", neturl.PathEscape(id), neturl.PathEscape(seedStr))
	if q := strings.TrimSpace(r.URL.RawQuery); q != "" {
		imagePath += "?" + q
	}
	title := meta.Name
	if title == "" {
		title = meta.OriginalFilename
	}
	if title == "" {
		title = meta.ID
	}
	escTitle := html.EscapeString(title)
	escURL := html.EscapeString(imagePath)
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	switch mode {
	case "og":
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "<!doctype html><html lang=\"en\"><head><meta charset=\"utf-8\"><title>%s</title><meta property=\"og:image\" content=\"%s\"><meta property=\"og:title\" content=\"%s\"></head><body></body></html>", escTitle, escURL, escTitle)
	default:
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "<!doctype html><html lang=\"en\"><head><meta charset=\"utf-8\"><title>%s (seed %s)</title><meta property=\"og:image\" content=\"%s\"></head><body style=\"margin:0;display:flex;align-items:center;justify-content:center;background:#0f172a;color:#e2e8f0;min-height:100vh;\"><figure style=\"text-align:center;max-width:90vw;\"><img src=\"%s\" alt=\"Spectrum %s\" style=\"max-width:100%%;height:auto;box-shadow:0 12px 32px rgba(15,23,42,0.45);border-radius:16px;\"><figcaption style=\"margin-top:12px;font-family:system-ui,sans-serif;font-size:14px;letter-spacing:0.03em;text-transform:uppercase;opacity:0.8;\">Payload %s · Seed %s</figcaption></figure></body></html>", escTitle, html.EscapeString(seedStr), escURL, escURL, html.EscapeString(seedStr), escTitle, html.EscapeString(seedStr))
	}
}

func (h *PayloadHandlers) snapshotList() []types.PayloadListItem {
	if h == nil || h.service == nil {
		return []types.PayloadListItem{}
	}
	metas := h.service.List()
	sort.Slice(metas, func(i, j int) bool {
		if metas[i].CreatedAt.Equal(metas[j].CreatedAt) {
			return metas[i].ID > metas[j].ID
		}
		return metas[i].CreatedAt.After(metas[j].CreatedAt)
	})
	items := make([]types.PayloadListItem, 0, len(metas))
	for _, meta := range metas {
		items = append(items, types.PayloadListItem{Payload: meta, Variants: h.service.GetVariants(meta.ID)})
	}
	return items
}

func (h *PayloadHandlers) generateSpectrumVariant(meta *types.PayloadMeta, seed int64) ([]byte, error) {
	if h == nil || h.service == nil {
		return nil, fmt.Errorf("payload storage unavailable")
	}
	path, err := h.service.GetFilePath(meta.ID)
	if err != nil {
		return nil, err
	}
	original, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(original) == 0 {
		return original, nil
	}
	buf := make([]byte, len(original))
	copy(buf, original)
	rng := rand.New(rand.NewSource(seed))
	mutateCount := 4 + rng.Intn(8)
	for i := 0; i < mutateCount; i++ {
		applySpectrumMutation(buf, rng)
	}
	sig := make([]byte, 8)
	binary.BigEndian.PutUint64(sig, uint64(seed))
	if len(buf) >= len(sig) {
		stride := len(buf) / len(sig)
		if stride == 0 {
			stride = 1
		}
		idx := 0
		for _, b := range sig {
			buf[idx] ^= b
			idx += stride
			if idx >= len(buf) {
				idx = len(buf) - 1
			}
		}
	} else {
		for i := range buf {
			buf[i] ^= sig[i%len(sig)]
		}
	}
	return buf, nil
}

func (h *PayloadHandlers) servePayloadFile(w http.ResponseWriter, r *http.Request, meta *types.PayloadMeta, overrideType, disposition string) {
	f, info, err := h.service.OpenFile(meta.ID)
	if err != nil {
		http.Error(w, "payload not found", http.StatusNotFound)
		return
	}
	defer f.Close()
	if overrideType != "" {
		w.Header().Set("Content-Type", overrideType)
	} else if meta.MimeType != "" {
		w.Header().Set("Content-Type", meta.MimeType)
	}
	if disposition != "" {
		w.Header().Set("Content-Disposition", disposition)
	}
	mod := meta.UpdatedAt
	if mod.IsZero() {
		mod = meta.CreatedAt
	}
	if mod.IsZero() {
		mod = info.ModTime()
	}
	http.ServeContent(w, r, meta.OriginalFilename, mod, f)
}

func (h *PayloadHandlers) servePayloadCorrupt(w http.ResponseWriter, r *http.Request, meta *types.PayloadMeta) {
	f, _, err := h.service.OpenFile(meta.ID)
	if err != nil {
		http.Error(w, "payload not found", http.StatusNotFound)
		return
	}
	defer f.Close()
	buf := make([]byte, 8192)
	n, err := f.Read(buf)
	if err != nil && err != io.EOF {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if n == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	truncated := n / 2
	if truncated <= 0 {
		truncated = n
	}
	if meta.MimeType != "" {
		w.Header().Set("Content-Type", meta.MimeType)
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(truncated))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf[:truncated])
}

func (h *PayloadHandlers) servePayloadSlow(w http.ResponseWriter, r *http.Request, meta *types.PayloadMeta, pause time.Duration) {
	f, _, err := h.service.OpenFile(meta.ID)
	if err != nil {
		http.Error(w, "payload not found", http.StatusNotFound)
		return
	}
	defer f.Close()
	if meta.MimeType != "" {
		w.Header().Set("Content-Type", meta.MimeType)
	}
	w.Header().Set("Cache-Control", "no-store")
	buf := make([]byte, 32*1024)
	flusher, _ := w.(http.Flusher)
	for {
		n, err := f.Read(buf)
		if n > 0 {
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
			if pause > 0 {
				time.Sleep(pause)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
	}
}

func (h *PayloadHandlers) servePayloadError(w http.ResponseWriter, r *http.Request, meta *types.PayloadMeta) {
	apierror.WriteError(w, http.StatusInternalServerError, "payload_error", "simulated payload failure", map[string]string{"payload": meta.ID})
}

func (h *PayloadHandlers) servePayloadRange(w http.ResponseWriter, r *http.Request, meta *types.PayloadMeta) {
	f, info, err := h.service.OpenFile(meta.ID)
	if err != nil {
		http.Error(w, "payload not found", http.StatusNotFound)
		return
	}
	defer f.Close()
	size := info.Size()
	if size == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if meta.MimeType != "" {
		w.Header().Set("Content-Type", meta.MimeType)
	}
	w.Header().Set("Accept-Ranges", "bytes")
	rangeHeader := r.Header.Get("Range")
	var start, end int64
	if rangeHeader == "" {
		start = 0
		end = min64(size-1, (1<<20)-1)
	} else {
		var parseErr error
		start, end, parseErr = parseRangeHeader(rangeHeader, size)
		if parseErr != nil {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", size))
			http.Error(w, "invalid range", http.StatusRequestedRangeNotSatisfiable)
			return
		}
	}
	length := end - start + 1
	if length < 0 {
		length = 0
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))
	w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
	w.WriteHeader(http.StatusPartialContent)
	if length > 0 {
		_, _ = io.CopyN(w, f, length)
	}
}

func applySpectrumMutation(buf []byte, rng *rand.Rand) {
	if len(buf) == 0 {
		return
	}
	switch rng.Intn(6) {
	case 0:
		idx := rng.Intn(len(buf))
		buf[idx] = byte(rng.Intn(256))
	case 1:
		idx := rng.Intn(len(buf))
		bit := uint(rng.Intn(8))
		buf[idx] ^= byte(1 << bit)
	case 2:
		if len(buf) > 1 {
			idx := rng.Intn(len(buf) - 1)
			buf[idx], buf[idx+1] = buf[idx+1], buf[idx]
		}
	case 3:
		if len(buf) > 4 {
			maxSize := len(buf)
			if maxSize > 96 {
				maxSize = 96
			}
			if maxSize > 2 {
				size := 2 + rng.Intn(maxSize-1)
				if size >= len(buf) {
					size = len(buf) - 1
				}
				if size > 1 {
					start := rng.Intn(len(buf) - size)
					half := size / 2
					for i := 0; i < half; i++ {
						buf[start+i], buf[start+size-1-i] = buf[start+size-1-i], buf[start+i]
					}
				}
			}
		}
	case 4:
		idx := rng.Intn(len(buf))
		delta := rng.Intn(31) - 15
		v := int(buf[idx]) + delta
		if v < 0 {
			v = 0
		} else if v > 255 {
			v = 255
		}
		buf[idx] = byte(v)
	case 5:
		idx := rng.Intn(len(buf))
		buf[idx] = 0
	}
}

func mismatchMime(original string) string {
	if strings.HasPrefix(original, "image/") {
		return "image/jpeg"
	}
	if strings.HasPrefix(original, "video/") {
		return "video/mp4"
	}
	if strings.HasPrefix(original, "audio/") {
		return "audio/mpeg"
	}
	return "text/plain"
}

func parseRangeHeader(header string, size int64) (int64, int64, error) {
	header = strings.TrimSpace(header)
	if !strings.HasPrefix(strings.ToLower(header), "bytes=") {
		return 0, 0, fmt.Errorf("unsupported range")
	}
	spec := strings.TrimPrefix(header, "bytes=")
	spec = strings.TrimSpace(spec)
	parts := strings.SplitN(spec, ",", 2)
	if len(parts) == 0 {
		return 0, 0, fmt.Errorf("invalid range")
	}
	rangePart := strings.TrimSpace(parts[0])
	dash := strings.SplitN(rangePart, "-", 2)
	if len(dash) != 2 {
		return 0, 0, fmt.Errorf("invalid range")
	}
	var start, end int64
	var err error
	if dash[0] == "" {
		suffix, err := strconv.ParseInt(dash[1], 10, 64)
		if err != nil || suffix <= 0 {
			return 0, 0, fmt.Errorf("invalid range")
		}
		if suffix > size {
			suffix = size
		}
		start = size - suffix
		end = size - 1
	} else {
		start, err = strconv.ParseInt(dash[0], 10, 64)
		if err != nil || start < 0 {
			return 0, 0, fmt.Errorf("invalid range")
		}
		if dash[1] == "" {
			end = size - 1
		} else {
			end, err = strconv.ParseInt(dash[1], 10, 64)
			if err != nil {
				return 0, 0, fmt.Errorf("invalid range")
			}
		}
	}
	if start >= size {
		return 0, 0, fmt.Errorf("invalid range")
	}
	if end >= size {
		end = size - 1
	}
	if end < start {
		return 0, 0, fmt.Errorf("invalid range")
	}
	return start, end, nil
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func extractSpectrumParams(path, prefix string) (string, string, bool) {
	if prefix == "" {
		prefix = "/payload/spec/"
	}
	if !strings.HasPrefix(path, prefix) {
		return "", "", false
	}
	rest := strings.Trim(path[len(prefix):], "/")
	if rest == "" {
		return "", "", false
	}
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) < 2 {
		return "", "", false
	}
	id := strings.TrimSpace(parts[0])
	seed := strings.TrimSpace(parts[1])
	if id == "" || seed == "" {
		return "", "", false
	}
	return id, seed, true
}

func deriveSpectrumSeed(id, seedStr string) (int64, uint32, string) {
	normalized := strings.TrimSpace(seedStr)
	if normalized == "" {
		normalized = "0"
	}
	sum := sha256.Sum256([]byte(id + ":" + normalized))
	raw := binary.BigEndian.Uint64(sum[:8]) & 0x7fffffffffffffff
	if raw == 0 {
		raw = 1
	}
	token := sanitizeToken(normalized)
	if token == "" {
		token = fmt.Sprintf("s%x", sum[12:16])
	}
	flags := binary.BigEndian.Uint32(sum[8:12])
	etag := fmt.Sprintf(`W/"spec-%s-%08x"`, token, binary.BigEndian.Uint32(sum[12:16]))
	return int64(raw), flags, etag
}

type spectrumHeaderProfile struct {
	MIME         string
	Disposition  string
	Chunked      bool
	AcceptRanges bool
}

func computeSpectrumHeaders(meta *types.PayloadMeta, mimeOverride string, flags uint32) spectrumHeaderProfile {
	profile := spectrumHeaderProfile{AcceptRanges: true}
	if mimeOverride = strings.TrimSpace(mimeOverride); mimeOverride != "" {
		profile.MIME = mimeOverride
	} else {
		original := strings.TrimSpace(meta.MimeType)
		if original == "" {
			original = "application/octet-stream"
		}
		variant := (flags >> 5) & 0x3
		switch variant {
		case 1:
			profile.MIME = "application/octet-stream"
		case 2:
			profile.MIME = mismatchMime(original)
		case 3:
			profile.MIME = "text/plain; charset=utf-8"
		default:
			profile.MIME = original
		}
	}
	if profile.MIME == "" {
		profile.MIME = "application/octet-stream"
	}
	dispBits := flags & 0x3
	switch dispBits {
	case 1:
		profile.Disposition = "inline"
	case 2:
		profile.Disposition = "attachment"
	}
	profile.Chunked = (flags>>2)&0x1 == 1
	profile.AcceptRanges = (flags>>3)&0x1 == 1
	return profile
}

func writeChunked(w http.ResponseWriter, data []byte, chunkSize int) {
	if chunkSize <= 0 {
		chunkSize = 1024
	}
	flusher, _ := w.(http.Flusher)
	if flusher == nil {
		_, _ = w.Write(data)
		return
	}
	for offset := 0; offset < len(data); offset += chunkSize {
		end := offset + chunkSize
		if end > len(data) {
			end = len(data)
		}
		if _, err := w.Write(data[offset:end]); err != nil {
			return
		}
		flusher.Flush()
	}
}

func spectrumFilename(meta *types.PayloadMeta, seedStr string) string {
	base := meta.OriginalFilename
	if base == "" {
		base = meta.Name
	}
	if base == "" {
		base = meta.ID
	}
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	safeStem := sanitizeToken(stem)
	if safeStem == "" {
		safeStem = "payload"
	}
	safeSeed := sanitizeToken(seedStr)
	if safeSeed == "" {
		safeSeed = "seed"
	}
	if ext == "" {
		ext = ".bin"
	}
	return fmt.Sprintf("%s_spec_%s%s", safeStem, safeSeed, ext)
}

func sanitizeToken(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		case r == ' ' || r == '.' || r == '+':
			b.WriteByte('-')
		}
		if b.Len() >= 48 {
			break
		}
	}
	return strings.Trim(b.String(), "-_")
}
