package server

import (
	"bufio"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"html/template"
	"io"
	"log"
	mathrand "math/rand"
	"mime"
	"net"
	"net/http"
	neturl "net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"runtime"

	fpdf "github.com/go-pdf/fpdf"
	"github.com/gorilla/websocket"
	_ "github.com/jackc/pgx/v5/stdlib"

	"linkpeek/internal/apierror"
	appruntime "linkpeek/internal/app/runtime"
	"linkpeek/internal/auth"
	"linkpeek/internal/capture"
	"linkpeek/internal/config"
	domainauth "linkpeek/internal/domain/auth"
	domaincapture "linkpeek/internal/domain/capture"
	domainpayload "linkpeek/internal/domain/payload"
	domainretry "linkpeek/internal/domain/retry"
	domainscanner "linkpeek/internal/domain/scanner"
	domainsnippet "linkpeek/internal/domain/snippet"
	domaintunnel "linkpeek/internal/domain/tunnel"
	"linkpeek/internal/http/router"
	"linkpeek/internal/realtime"
	"linkpeek/internal/types"
	"linkpeek/internal/utils"
	"linkpeek/middleware"
)

func savePayloadMeta(meta *types.PayloadMeta) error {
	if meta == nil {
		return fmt.Errorf("nil payload meta")
	}
	if meta.ID == "" {
		return fmt.Errorf("payload meta missing id")
	}
	meta.UpdatedAt = utils.NowUTC()
	b, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(payloadMetaPath(meta.ID), b, 0o644)
}

func loadPayloadMeta(id string) (*types.PayloadMeta, error) {
	b, err := os.ReadFile(payloadMetaPath(id))
	if err != nil {
		return nil, err
	}
	var meta types.PayloadMeta
	if err := json.Unmarshal(b, &meta); err != nil {
		return nil, err
	}
	if meta.ID == "" {
		meta.ID = id
	}
	return &meta, nil
}

func loadPayloadIndex() {
	payloadMu.Lock()
	defer payloadMu.Unlock()
	payloads = map[string]*types.PayloadMeta{}
	entries, err := os.ReadDir(payloadDir)
	if err != nil {
		log.Printf("payload index read error: %v", err)
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		id := entry.Name()
		meta, err := loadPayloadMeta(id)
		if err != nil {
			log.Printf("payload meta load error (%s): %v", id, err)
			continue
		}
		payloads[id] = meta
	}
}

func setPayloadMeta(meta *types.PayloadMeta) {
	payloadMu.Lock()
	defer payloadMu.Unlock()
	payloads[meta.ID] = meta
}

func deletePayloadMeta(id string) {
	payloadMu.Lock()
	defer payloadMu.Unlock()
	delete(payloads, id)
}

func getPayloadMeta(id string) (*types.PayloadMeta, bool) {
	payloadMu.RLock()
	defer payloadMu.RUnlock()
	meta, ok := payloads[id]
	if !ok {
		return nil, false
	}
	copyMeta := *meta
	return &copyMeta, true
}

func listPayloads() []types.PayloadMeta {
	payloadMu.RLock()
	defer payloadMu.RUnlock()
	out := make([]types.PayloadMeta, 0, len(payloads))
	for _, meta := range payloads {
		out = append(out, *meta)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out
}

func payloadVariantsFor(id string) []types.PayloadVariant {
	return []types.PayloadVariant{
		{Key: "raw", Path: fmt.Sprintf("/payload/raw/%s", id)},
		{Key: "inline", Path: fmt.Sprintf("/payload/inline/%s", id)},
		{Key: "download", Path: fmt.Sprintf("/payload/download/%s", id)},
		{Key: "mime_mismatch", Path: fmt.Sprintf("/payload/mime-mismatch/%s", id)},
		{Key: "corrupt", Path: fmt.Sprintf("/payload/corrupt/%s", id)},
		{Key: "slow", Path: fmt.Sprintf("/payload/slow/%s", id)},
		{Key: "chunked", Path: fmt.Sprintf("/payload/chunked/%s", id)},
		{Key: "redirect", Path: fmt.Sprintf("/payload/redirect/%s", id)},
		{Key: "error", Path: fmt.Sprintf("/payload/error/%s", id)},
		{Key: "range", Path: fmt.Sprintf("/payload/range/%s", id)},
		{Key: "spectrum", Path: fmt.Sprintf("/payload/spec/%s/0", id)},
	}
}

func snapshotPayloadList() []types.PayloadListItem {
	list := listPayloads()
	items := make([]types.PayloadListItem, 0, len(list))
	for _, meta := range list {
		items = append(items, types.PayloadListItem{Payload: meta, Variants: payloadVariantsFor(meta.ID)})
	}
	return items
}

func publishPayloadList() {
	if payloadSvc != nil {
		payloadSvc.PublishList()
		return
	}
	if realtimeHub == nil {
		return
	}
	realtimeHub.Publish("payload.list", snapshotPayloadList())
}

var (
	recent                = types.NewEventBuffer(200)
	dataDir               = "/data"
	dbConn                *sql.DB
	allowTunnelAdmin      bool
	cloudflaredContainer  string
	payloadDir            string
	payloadMu             sync.RWMutex
	payloads                    = map[string]*types.PayloadMeta{}
	payloadMaxUploadBytes int64 = 250 << 20
	// HTTP client with sane defaults for any outbound calls
	httpClient = &http.Client{Timeout: 30 * time.Second}
	startedAt  = time.Now()
	// CPU usage sampling
	cpuSampleMu        sync.Mutex
	lastProcCPUSeconds float64
	lastWall           time.Time
	// rate limiting (login firewall)
	loginRateLimiter *middleware.RateLimiter
	// auth
	sessions  *sessionManager
	authStore *auth.Store
	// capture hooks
	captureManager *capture.Manager
	payloadSvc     *domainpayload.Service
	scannerSvc     *domainscanner.Service
	tunnelSvc      *domaintunnel.Service
	retryLabSvc    *domainretry.Lab
	// realtime hub
	realtimeHub     *realtime.Hub
	realtimeEnabled = true
	wsUpgrader      = websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin:     checkWebSocketOrigin,
	}
)

const (
	retryLabHeader      = "X-RetryLab-Scenario"
	captureHookHeader   = "X-Capture-Hook"
	payloadMetaFilename = "meta.json"
	payloadDataFilename = "original.bin"
)

func newPayloadID() string {
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err == nil {
		return "p-" + hex.EncodeToString(buf)
	}
	return fmt.Sprintf("p-%d", time.Now().UnixNano())
}

func payloadMetaPath(id string) string {
	return filepath.Join(payloadDir, id, payloadMetaFilename)
}

func payloadDataPath(id string) string {
	return filepath.Join(payloadDir, id, payloadDataFilename)
}

func respondJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func markCaptureRequest(r *http.Request, hookID string) {
	if r == nil || hookID == "" {
		return
	}
	r.Header.Set(captureHookHeader, hookID)
}

// SSE subscribers
type subscriber chan types.Event

var (
	subsMu sync.RWMutex
	subs   = map[subscriber]struct{}{}
)

func subscribe() subscriber {
	ch := make(chan types.Event, 16)
	subsMu.Lock()
	subs[ch] = struct{}{}
	subsMu.Unlock()
	return ch
}

func unsubscribe(ch subscriber) {
	subsMu.Lock()
	if _, ok := subs[ch]; ok {
		delete(subs, ch)
		close(ch)
	}
	subsMu.Unlock()
}

func broadcast(e types.Event) {
	subsMu.RLock()
	defer subsMu.RUnlock()
	for ch := range subs {
		select {
		case ch <- e:
		default:
			// drop if slow
		}
	}
}

func ensureDataDir() error {
	if d := os.Getenv("DATA_DIR"); d != "" {
		dataDir = d
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("failed to create data dir %s: %w", dataDir, err)
	}
	payloadDir = filepath.Join(dataDir, "payloads")
	if err := os.MkdirAll(payloadDir, 0o755); err != nil {
		return fmt.Errorf("failed to create payload dir %s: %w", payloadDir, err)
	}
	if payloadMaxUploadBytes <= 0 {
		payloadMaxUploadBytes = 250 << 20
	}
	if v := strings.TrimSpace(os.Getenv("PAYLOAD_MAX_UPLOAD_MB")); v != "" {
		if mb, err := strconv.ParseInt(v, 10, 64); err == nil && mb > 0 {
			payloadMaxUploadBytes = mb * 1024 * 1024
		}
	}
	if v := strings.TrimSpace(os.Getenv("ALLOW_TUNNEL_ADMIN")); v != "" {
		allowTunnelAdmin = v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "yes")
	}
	if v := strings.TrimSpace(os.Getenv("CLOUDFLARED_CONTAINER")); v != "" {
		cloudflaredContainer = v
	}
	return nil
}

// --- DB section (optional) ---
func dbInit() {
	dsn := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if dsn == "" {
		return
	}
	var err error
	dbConn, err = sql.Open("pgx", dsn)
	if err != nil {
		log.Printf("db open error: %v", err)
		dbConn = nil
		return
	}
	dbConn.SetMaxOpenConns(10)
	dbConn.SetMaxIdleConns(5)
	dbConn.SetConnMaxLifetime(30 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := dbConn.PingContext(ctx); err != nil {
		log.Printf("db ping error: %v", err)
		_ = dbConn.Close()
		dbConn = nil
		return
	}
	// Minimal schema
	ddl := `
CREATE TABLE IF NOT EXISTS ip_profile (
  id BIGSERIAL PRIMARY KEY,
  ip INET UNIQUE NOT NULL,
  first_seen TIMESTAMPTZ NOT NULL,
  last_seen  TIMESTAMPTZ NOT NULL,
  req_count  BIGINT NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS user_agent (
  id BIGSERIAL PRIMARY KEY,
  ua TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS request (
  id BIGSERIAL PRIMARY KEY,
  ts TIMESTAMPTZ NOT NULL,
  ip INET NOT NULL,
  method TEXT,
  path TEXT,
  query TEXT,
  status INT,
  duration_ms BIGINT,
  request_id TEXT,
    ua_id BIGINT REFERENCES user_agent(id),
    sess TEXT,
    sfu TEXT, -- Sec-Fetch-User
    sfm TEXT, -- Sec-Fetch-Mode
    sfd TEXT, -- Sec-Fetch-Dest
    sfs TEXT, -- Sec-Fetch-Site
    referer TEXT,
    origin TEXT
);
CREATE INDEX IF NOT EXISTS idx_request_ip_ts ON request(ip, ts DESC);
CREATE INDEX IF NOT EXISTS idx_request_ua ON request(ua_id);
CREATE INDEX IF NOT EXISTS idx_request_sess ON request(sess);

CREATE TABLE IF NOT EXISTS ip_geo (
    ip INET PRIMARY KEY,
    json JSONB NOT NULL,
    fetched_at TIMESTAMPTZ NOT NULL
);
`
	if _, err := dbConn.ExecContext(ctx, ddl); err != nil {
		log.Printf("db schema error: %v", err)
	}
	// best-effort migrations for older DBs
	_, _ = dbConn.ExecContext(context.Background(), `ALTER TABLE request ADD COLUMN IF NOT EXISTS sess TEXT`)
	_, _ = dbConn.ExecContext(context.Background(), `CREATE INDEX IF NOT EXISTS idx_request_sess ON request(sess)`)
	_, _ = dbConn.ExecContext(context.Background(), `ALTER TABLE request ADD COLUMN IF NOT EXISTS sfu TEXT`)
	_, _ = dbConn.ExecContext(context.Background(), `ALTER TABLE request ADD COLUMN IF NOT EXISTS sfm TEXT`)
	_, _ = dbConn.ExecContext(context.Background(), `ALTER TABLE request ADD COLUMN IF NOT EXISTS sfd TEXT`)
	_, _ = dbConn.ExecContext(context.Background(), `ALTER TABLE request ADD COLUMN IF NOT EXISTS sfs TEXT`)
	_, _ = dbConn.ExecContext(context.Background(), `ALTER TABLE request ADD COLUMN IF NOT EXISTS referer TEXT`)
	_, _ = dbConn.ExecContext(context.Background(), `ALTER TABLE request ADD COLUMN IF NOT EXISTS origin TEXT`)
}

func dbPing() (ok bool, msg string) {
	if dbConn == nil {
		return false, "DB disabled"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := dbConn.PingContext(ctx); err != nil {
		return false, err.Error()
	}
	return true, "ok"
}

func writeEventJSONL(e types.Event) {
	path := filepath.Join(dataDir, "events.jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		log.Printf("writeEventJSONL open error: %v", err)
		return
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	if err := enc.Encode(e); err != nil {
		log.Printf("writeEventJSONL encode error: %v", err)
	}
}

// Private/internal networks to filter out (no logging/streaming)
var privateCIDRs = mustCIDRs(
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
	"127.0.0.0/8",
	"169.254.0.0/16",
	"::1/128",
	"fc00::/7",
	"fe80::/10",
)

func mustCIDRs(cidrs ...string) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err == nil {
			out = append(out, n)
		}
	}
	return out
}

func isPrivateIP(ipStr string) bool {
	ip := net.ParseIP(strings.TrimSpace(ipStr))
	if ip == nil {
		return false
	}
	for _, n := range privateCIDRs {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// middleware to capture events
func withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		lrw := &loggingResponseWriter{ResponseWriter: w, statusCode: 200}
		reqID := utils.ReqID(start)
		w.Header().Set("X-Request-Id", reqID)

		next.ServeHTTP(lrw, r)

		dur := time.Since(start).Milliseconds()
		e := types.Event{
			Ts:         utils.NowUTC(),
			Method:     r.Method,
			Path:       r.URL.Path,
			Query:      r.URL.RawQuery,
			RemoteIP:   utils.RemoteIP(r),
			UA:         r.UserAgent(),
			Status:     lrw.statusCode,
			DurationMs: dur,
			RequestID:  reqID,
			Headers:    utils.HeaderMap(r),
			Sess:       tokenFromRequest(r),
			SFU:        r.Header.Get("Sec-Fetch-User"),
			SFM:        r.Header.Get("Sec-Fetch-Mode"),
			SFD:        r.Header.Get("Sec-Fetch-Dest"),
			SFS:        r.Header.Get("Sec-Fetch-Site"),
			Referer:    r.Referer(),
			Origin:     r.Header.Get("Origin"),
		}
		if scenario := strings.TrimSpace(r.Header.Get(retryLabHeader)); scenario != "" {
			e.Class = "retrylab"
			if e.Headers == nil {
				e.Headers = map[string]string{}
			}
			e.Headers[strings.ToLower(retryLabHeader)] = scenario
		}
		if hookID := strings.TrimSpace(r.Header.Get(captureHookHeader)); hookID != "" {
			e.Class = "capture"
			if e.Headers == nil {
				e.Headers = map[string]string{}
			}
			e.Headers[strings.ToLower(captureHookHeader)] = hookID
		}
		// Filter only if it's clearly internal: drop when IP is private AND no proxy headers indicate a real client.
		if !(isPrivateIP(e.RemoteIP) && r.Header.Get("X-Forwarded-For") == "" && r.Header.Get("CF-Connecting-IP") == "" && r.Header.Get("Forwarded") == "") {
			recent.Add(e)
			writeEventJSONL(e)
			broadcast(e)
			// Publish to realtime hub
			if realtimeHub != nil {
				realtimeHub.Publish("log.event", e)
			}
			// async DB insert (best-effort)
			if dbConn != nil {
				go dbInsertEvent(e)
			}
		}
	})
}

// withSecurityHeaders sets basic security headers on every response.
func withSecurityHeaders(next http.Handler) http.Handler {
	// A conservative CSP that allows our inline styles/scripts already present in templates
	// If you later remove inline usage, drop 'unsafe-inline'.
	const csp = "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self' data:; frame-ancestors 'none'"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "geolocation=(), microphone=(), camera=()")
		// Only set CSP for HTML responses; for simplicity set globally except static assets (served earlier in chain)
		// If downstream sets Content-Security-Policy explicitly, don't override.
		if w.Header().Get("Content-Security-Policy") == "" {
			w.Header().Set("Content-Security-Policy", csp)
		}
		next.ServeHTTP(w, r)
	})
}

// gzipResponseWriter wraps http.ResponseWriter to support gzip compression
type gzipResponseWriter struct {
	io.Writer
	http.ResponseWriter
}

func (w gzipResponseWriter) Write(b []byte) (int, error) {
	return w.Writer.Write(b)
}

// withGzip adds gzip compression for responses when client supports it
func withGzip(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Never wrap WebSocket upgrades; the upgrader requires the raw response writer
		if websocket.IsWebSocketUpgrade(r) {
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/retrylab/") {
			next.ServeHTTP(w, r)
			return
		}
		// Check if client accepts gzip encoding
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}

		// Only compress text-based content types
		// We'll set the header after we know the content type
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Vary", "Accept-Encoding")

		gz := gzip.NewWriter(w)
		defer gz.Close()

		gzw := gzipResponseWriter{Writer: gz, ResponseWriter: w}
		next.ServeHTTP(gzw, r)
	})
}

func withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions || isPublicPath(r) {
			next.ServeHTTP(w, r)
			return
		}

		if isTunnelHost(r.Host) {
			msg := "Admin UI is unavailable over the shared tunnel. Access LinkPeek from the trusted control host."
			if isAPIRequest(r) {
				apierror.WriteError(w, http.StatusForbidden, "forbidden", msg, nil)
			} else {
				http.Error(w, msg, http.StatusForbidden)
			}
			return
		}

		token := readSessionToken(r)
		if ok, expires := sessions.Validate(token); ok {
			// Keep cookie aligned with session expiry on active use.
			setSessionCookie(w, token, expires, isSecureRequest(r))
			next.ServeHTTP(w, r)
			return
		}
		if token != "" {
			sessions.Delete(token)
			clearSessionCookie(w)
		}

		if isAPIRequest(r) {
			apierror.WriteError(w, http.StatusUnauthorized, "unauthorized", "login required", nil)
			return
		}

		loginURL := "/login"
		if nxt := sanitizeNext(r.URL.RequestURI()); nxt != "" {
			loginURL = fmt.Sprintf("/login?next=%s", neturl.QueryEscape(nxt))
		}
		http.Redirect(w, r, loginURL, http.StatusSeeOther)
	})
}

func isPublicPath(r *http.Request) bool {
	path := r.URL.Path
	switch {
	case path == "/":
		return true
	case path == "/login":
		return true
	case path == "/logout":
		return true
	case path == "/api/auth/status":
		return true
	case path == "/healthz":
		return true
	case path == "/favicon.ico":
		return true
	case strings.HasPrefix(path, "/static/"):
		return true
	case strings.HasPrefix(path, "/alpha"):
		return true
	case strings.HasPrefix(path, "/payload/"):
		return true
	case strings.HasPrefix(path, "/snippet/"):
		return true
	case strings.HasPrefix(path, "/capture/"):
		return true
	default:
		return false
	}
}

func isAPIRequest(r *http.Request) bool {
	return strings.HasPrefix(r.URL.Path, "/api/")
}

func sanitizeNext(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "//") {
		return ""
	}
	u, err := neturl.Parse(raw)
	if err != nil {
		return ""
	}
	if u.IsAbs() {
		return ""
	}
	if !strings.HasPrefix(u.Path, "/") {
		return ""
	}
	if u.Path == "/login" || u.Path == "/logout" {
		return ""
	}
	u.Scheme = ""
	u.Host = ""
	u.Fragment = ""
	return u.String()
}

// tokenFromRequest extracts the alpha session token (cookie "lp" or query param "t")
func tokenFromRequest(r *http.Request) string {
	if c, err := r.Cookie("lp"); err == nil && c.Value != "" {
		return c.Value
	}
	if v := strings.TrimSpace(r.URL.Query().Get("t")); v != "" {
		return v
	}
	return ""
}

const sessionCookieName = "lp_session"

type sessionManager struct {
	mu     sync.RWMutex
	ttl    time.Duration
	items  map[string]time.Time
	stopCh chan struct{}
}

func newSessionManager(ttl time.Duration) *sessionManager {
	sm := &sessionManager{
		ttl:    ttl,
		items:  make(map[string]time.Time),
		stopCh: make(chan struct{}),
	}
	go sm.cleanup()
	return sm
}

func (sm *sessionManager) Create() (string, time.Time) {
	if sm == nil {
		return "", time.Time{}
	}
	token := newSessionToken()
	expires := time.Now().Add(sm.ttl)
	sm.mu.Lock()
	sm.items[token] = expires
	sm.mu.Unlock()
	return token, expires
}

func (sm *sessionManager) Validate(token string) (bool, time.Time) {
	if sm == nil || token == "" {
		return false, time.Time{}
	}
	sm.mu.Lock()
	expires, ok := sm.items[token]
	if !ok {
		sm.mu.Unlock()
		return false, time.Time{}
	}
	if time.Now().After(expires) {
		delete(sm.items, token)
		sm.mu.Unlock()
		return false, time.Time{}
	}
	expires = time.Now().Add(sm.ttl)
	sm.items[token] = expires
	sm.mu.Unlock()
	return true, expires
}

func (sm *sessionManager) Delete(token string) {
	if sm == nil || token == "" {
		return
	}
	sm.mu.Lock()
	delete(sm.items, token)
	sm.mu.Unlock()
}

func (sm *sessionManager) cleanup() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			now := time.Now()
			sm.mu.Lock()
			for token, exp := range sm.items {
				if now.After(exp) {
					delete(sm.items, token)
				}
			}
			sm.mu.Unlock()
		case <-sm.stopCh:
			return
		}
	}
}

func (sm *sessionManager) Close() {
	if sm == nil {
		return
	}
	sm.mu.Lock()
	if sm.stopCh != nil {
		close(sm.stopCh)
		sm.stopCh = nil
	}
	sm.mu.Unlock()
}

func newSessionToken() string {
	buf := make([]byte, 18)
	if _, err := rand.Read(buf); err == nil {
		return hex.EncodeToString(buf)
	}
	return fmt.Sprintf("s-%d", time.Now().UnixNano())
}

func readSessionToken(r *http.Request) string {
	if c, err := r.Cookie(sessionCookieName); err == nil && c.Value != "" {
		return c.Value
	}
	return ""
}

func setSessionCookie(w http.ResponseWriter, token string, expires time.Time, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  expires,
		Secure:   secure,
	})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		SameSite: http.SameSiteLaxMode,
	})
}

func dbInsertEvent(e types.Event) {
	if dbConn == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// upsert user_agent
	var uaID int64
	if err := dbConn.QueryRowContext(ctx, `SELECT id FROM user_agent WHERE ua=$1`, e.UA).Scan(&uaID); err != nil {
		if err == sql.ErrNoRows {
			_ = dbConn.QueryRowContext(ctx, `INSERT INTO user_agent(ua) VALUES($1) RETURNING id`, e.UA).Scan(&uaID)
		}
	}
	// upsert ip_profile
	now := e.Ts
	if _, err := dbConn.ExecContext(ctx, `
INSERT INTO ip_profile(ip, first_seen, last_seen, req_count)
VALUES ($1,$2,$2,1)
ON CONFLICT (ip) DO UPDATE SET last_seen=EXCLUDED.last_seen, req_count=ip_profile.req_count+1
`, e.RemoteIP, now); err != nil {
		// log softly
		// log.Printf("ip_profile upsert error: %v", err)
	}
	// insert request row
	_, _ = dbConn.ExecContext(ctx, `
INSERT INTO request(ts, ip, method, path, query, status, duration_ms, request_id, ua_id, sess, sfu, sfm, sfd, sfs, referer, origin)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
`, e.Ts, e.RemoteIP, e.Method, e.Path, e.Query, e.Status, e.DurationMs, e.RequestID, nullableID(uaID), e.Sess, e.SFU, e.SFM, e.SFD, e.SFS, e.Referer, e.Origin)

	// Opportunistic geo prefetch for new IPs (IPv4/IPv6 supported).
	// Runs best-effort in background and stores once per IP.
	go prefetchGeoIfAbsent(e.RemoteIP)
}

func nullableID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

type loggingResponseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (lrw *loggingResponseWriter) WriteHeader(code int) {
	lrw.statusCode = code
	lrw.ResponseWriter.WriteHeader(code)
}

// Support http.Flusher when the underlying writer supports it (needed for SSE)
func (lrw *loggingResponseWriter) Flush() {
	if f, ok := lrw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Support http.Hijacker so websocket upgrades succeed through the logging wrapper
func (lrw *loggingResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := lrw.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, fmt.Errorf("http.Hijacker not supported")
}

func handleDBPing(w http.ResponseWriter, r *http.Request) {
	ok, msg := dbPing()
	w.Header().Set("Content-Type", "application/json")
	if ok {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
		return
	}
	apierror.WriteError(w, http.StatusServiceUnavailable, "db_unavailable", msg, nil)
}

func handleEcho(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	body := map[string]any{
		"method":  r.Method,
		"path":    r.URL.Path,
		"query":   r.URL.RawQuery,
		"headers": r.Header,
		"ts":      utils.NowUTC(),
	}
	json.NewEncoder(w).Encode(body)
}

func handleEvents(w http.ResponseWriter, r *http.Request) {
	n := 50
	if s := r.URL.Query().Get("n"); s != "" {
		if v, err := strconv.Atoi(s); err == nil && v > 0 && v <= 200 {
			n = v
		}
	}
	items := recent.Last(n)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(items)
}

func handleEventsStream(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "stream unsupported", http.StatusInternalServerError)
		return
	}
	ch := subscribe()
	defer unsubscribe(ch)

	// bootstrap with last few events
	boot := recent.Last(10)
	for _, e := range boot {
		b, _ := json.Marshal(e)
		fmt.Fprintf(w, "data: %s\n\n", b)
	}
	flusher.Flush()

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case e := <-ch:
			b, _ := json.Marshal(e)
			fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
		case <-ticker.C:
			// comment ping to keep connection alive
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// checkWebSocketOrigin validates the origin header for WebSocket connections
func checkWebSocketOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true // Allow connections without Origin header (e.g., non-browser clients)
	}
	originURL, err := neturl.Parse(origin)
	if err != nil {
		return false
	}
	// Allow same-origin requests
	return originURL.Host == r.Host
}

// handleRealtime upgrades HTTP connection to WebSocket and handles realtime events
func handleRealtime(w http.ResponseWriter, r *http.Request) {
	if !realtimeEnabled || realtimeHub == nil {
		http.Error(w, "realtime service unavailable", http.StatusServiceUnavailable)
		return
	}

	// Upgrade to WebSocket
	ws, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("websocket upgrade error: %v", err)
		return
	}

	// Create and register client
	conn := realtime.NewWSConn(ws)
	client := realtimeHub.RegisterClient(conn)
	defer client.Close()

	// Start write pump
	go client.WritePump()

	// Handle incoming messages
	ws.SetReadDeadline(time.Now().Add(60 * time.Second))
	ws.SetPongHandler(func(string) error {
		ws.SetReadDeadline(time.Now().Add(60 * time.Second))
		client.UpdateHeartbeat()
		return nil
	})

	// Send ping every 30 seconds
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	go func() {
		for range ticker.C {
			if err := ws.WriteControl(websocket.PingMessage, []byte{}, time.Now().Add(10*time.Second)); err != nil {
				return
			}
		}
	}()

	for {
		var msg map[string]interface{}
		err := ws.ReadJSON(&msg)
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("websocket read error: %v", err)
			}
			return
		}

		action, _ := msg["action"].(string)
		switch action {
		case "subscribe":
			if topics, ok := msg["topics"].([]interface{}); ok {
				for _, t := range topics {
					if topic, ok := t.(string); ok {
						if err := client.Subscribe(topic); err != nil {
							client.SendError("subscription failed: " + err.Error())
							continue
						}
					}
				}
			}
		case "unsubscribe":
			if topics, ok := msg["topics"].([]interface{}); ok {
				for _, t := range topics {
					if topic, ok := t.(string); ok {
						client.Unsubscribe(topic)
					}
				}
			}
		case "snapshot":
			if topic, ok := msg["topic"].(string); ok {
				snapshot, err := realtimeHub.GetSnapshot(topic)
				if err != nil {
					client.SendError("snapshot error: " + err.Error())
				} else if snapshot != nil {
					client.SendSnapshot(topic, snapshot)
				}
			}
		case "ping":
			client.UpdateHeartbeat()
		default:
			client.SendError("unknown action")
		}
	}
}

// ---- IP analytics API ----

func handleIPs(w http.ResponseWriter, r *http.Request) {
	if dbConn == nil {
		http.Error(w, "db disabled", http.StatusServiceUnavailable)
		return
	}
	q := r.URL.Query()
	search := strings.TrimSpace(q.Get("search"))
	sort := q.Get("sort")
	if sort == "" {
		sort = "last_seen"
	}
	desc := true
	if v := q.Get("desc"); v != "" {
		if v == "0" || strings.EqualFold(v, "false") {
			desc = false
		}
	}
	limit := clamp(parseIntDefault(q.Get("limit"), 50), 1, 200)
	offset := clamp(parseIntDefault(q.Get("offset"), 0), 0, 1000000)

	// Build ORDER BY safely
	order := "last_seen"
	switch sort {
	case "last_seen":
		order = "last_seen"
	case "req_count":
		order = "req_count"
	default:
		order = "last_seen"
	}
	dir := "DESC"
	if !desc {
		dir = "ASC"
	}

	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	var rows *sql.Rows
	var err error
	if search == "" {
		rows, err = dbConn.QueryContext(ctx,
			"SELECT ip::text, first_seen, last_seen, req_count FROM ip_profile ORDER BY "+order+" "+dir+" LIMIT $1 OFFSET $2",
			limit, offset,
		)
	} else {
		like := "%" + search + "%"
		rows, err = dbConn.QueryContext(ctx,
			"SELECT ip::text, first_seen, last_seen, req_count FROM ip_profile WHERE ip::text ILIKE $1 ORDER BY "+order+" "+dir+" LIMIT $2 OFFSET $3",
			like, limit, offset,
		)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	list := make([]types.IpProfile, 0, limit)
	for rows.Next() {
		var p types.IpProfile
		if err := rows.Scan(&p.IP, &p.FirstSeen, &p.LastSeen, &p.ReqCount); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		list = append(list, p)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"items":  list,
		"offset": offset,
		"limit":  limit,
	})
}

func handleIPSummary(w http.ResponseWriter, r *http.Request) {
	if dbConn == nil {
		http.Error(w, "db disabled", http.StatusServiceUnavailable)
		return
	}
	ip, ok := extractIPFromPath(r.URL.Path, "/api/ip/", "/summary")
	if !ok {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	bucket := q.Get("bucket")
	switch bucket {
	case "minute", "hour", "day":
		// ok
	default:
		bucket = "hour"
	}
	// range parameter (duration), default 24h
	since := time.Now().Add(-24 * time.Hour)
	if rv := strings.TrimSpace(q.Get("range")); rv != "" {
		if d, err := parseDurationHuman(rv); err == nil {
			since = time.Now().Add(-d)
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()

	// UA breakdown
	uaRows, err := dbConn.QueryContext(ctx, `
        SELECT COALESCE(u.ua,'') AS ua, COUNT(*)
        FROM request r LEFT JOIN user_agent u ON u.id=r.ua_id
        WHERE r.ip = $1 AND r.ts >= $2
        GROUP BY ua
        ORDER BY COUNT(*) DESC
        LIMIT 20`, ip, since)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer uaRows.Close()
	var uaList []map[string]any
	for uaRows.Next() {
		var ua string
		var c int64
		if err := uaRows.Scan(&ua, &c); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		uaList = append(uaList, map[string]any{"ua": ua, "count": c})
	}

	// Top paths
	pathRows, err := dbConn.QueryContext(ctx, `
        SELECT r.path, COUNT(*)
        FROM request r
        WHERE r.ip = $1 AND r.ts >= $2
        GROUP BY r.path
        ORDER BY COUNT(*) DESC
        LIMIT 20`, ip, since)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer pathRows.Close()
	var paths []map[string]any
	for pathRows.Next() {
		var p string
		var c int64
		if err := pathRows.Scan(&p, &c); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		paths = append(paths, map[string]any{"path": p, "count": c})
	}

	// Timeline counts
	tlRows, err := dbConn.QueryContext(ctx, `
        SELECT date_trunc($1, ts) AS bucket, COUNT(*)
        FROM request
        WHERE ip = $2 AND ts >= $3
        GROUP BY bucket
        ORDER BY bucket`, bucket, ip, since)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer tlRows.Close()
	var timeline []types.TimelinePoint
	for tlRows.Next() {
		var t time.Time
		var c int64
		if err := tlRows.Scan(&t, &c); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		timeline = append(timeline, types.TimelinePoint{T: t, C: c})
	}

	// Classes
	classes, _ := buildClassCounts(ctx, ip, since)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(types.IpSummary{UA: uaList, Paths: paths, Timeline: timeline, Classes: classes})
}

func handleIPRequests(w http.ResponseWriter, r *http.Request) {
	if dbConn == nil {
		http.Error(w, "db disabled", http.StatusServiceUnavailable)
		return
	}
	ip, ok := extractIPFromPath(r.URL.Path, "/api/ip/", "/requests")
	if !ok {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	limit := clamp(parseIntDefault(q.Get("limit"), 100), 1, 500)
	offset := clamp(parseIntDefault(q.Get("offset"), 0), 0, 1000000)

	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()
	rows, err := dbConn.QueryContext(ctx, `
        SELECT r.ts, r.method, r.path, r.query, r.status, r.duration_ms, r.request_id, COALESCE(u.ua,'')
        FROM request r LEFT JOIN user_agent u ON u.id=r.ua_id
        WHERE r.ip = $1
        ORDER BY r.ts DESC
        LIMIT $2 OFFSET $3`, ip, limit, offset)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	type row struct {
		Ts         time.Time `json:"ts"`
		Method     string    `json:"method"`
		Path       string    `json:"path"`
		Query      string    `json:"query"`
		Status     int       `json:"status"`
		DurationMs int64     `json:"duration_ms"`
		RequestID  string    `json:"request_id"`
		UA         string    `json:"ua"`
	}
	out := make([]row, 0, limit)
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.Ts, &x.Method, &x.Path, &x.Query, &x.Status, &x.DurationMs, &x.RequestID, &x.UA); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out = append(out, x)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"items": out, "offset": offset, "limit": limit})
}

// ---- IP sizes / export / delete ----

func handleIPSizes(w http.ResponseWriter, r *http.Request) {
	if dbConn == nil {
		http.Error(w, "db disabled", http.StatusServiceUnavailable)
		return
	}
	ip, ok := extractIPFromPath(r.URL.Path, "/api/ip/", "/sizes")
	if !ok {
		http.NotFound(w, r)
		return
	}
	res := types.IpSizes{}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	// Geo size
	var geoBytes sql.NullInt64
	if err := dbConn.QueryRowContext(ctx, `SELECT COALESCE(octet_length(json::text),0) FROM ip_geo WHERE ip=$1`, ip).Scan(&geoBytes); err == nil {
		res.Geo.Bytes = geoBytes.Int64
		res.Geo.Present = geoBytes.Valid
	}
	// Requests count
	_ = dbConn.QueryRowContext(ctx, `SELECT COUNT(*) FROM request WHERE ip=$1`, ip).Scan(&res.Requests.Rows)
	// UA count (distinct UA strings for this IP)
	_ = dbConn.QueryRowContext(ctx, `SELECT COUNT(DISTINCT COALESCE(u.ua,'')) FROM request r LEFT JOIN user_agent u ON u.id=r.ua_id WHERE r.ip=$1`, ip).Scan(&res.UA.Count)
	// Summary bytes (approx by marshalling current summary result)
	sum, err := buildIPSummary(ctx, ip, "hour", 24*time.Hour)
	if err == nil {
		if b, e := json.Marshal(sum); e == nil {
			res.Summary.Bytes = int64(len(b))
		}
	}
	// Aggregate estimate for full export (geo + summary + requests list + UA list)
	// We avoid materializing everything; use a simple heuristic for bytes per row.
	const avgReqBytes = 200 // rough JSON bytes per request row
	const avgUABits = 64    // rough JSON bytes per UA entry
	res.All.BytesEstimate = res.Geo.Bytes + res.Summary.Bytes + res.Requests.Rows*avgReqBytes + res.UA.Count*avgUABits
	res.All.Estimated = true
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(res)
}

// Reuse summary building for sizes/export
func buildIPSummary(ctx context.Context, ip, bucket string, sinceDur time.Duration) (types.IpSummary, error) {
	since := time.Now().Add(-sinceDur)
	// UA breakdown
	uaRows, err := dbConn.QueryContext(ctx, `
        SELECT COALESCE(u.ua,'') AS ua, COUNT(*)
        FROM request r LEFT JOIN user_agent u ON u.id=r.ua_id
        WHERE r.ip = $1 AND r.ts >= $2
        GROUP BY ua
        ORDER BY COUNT(*) DESC
        LIMIT 20`, ip, since)
	if err != nil {
		return types.IpSummary{}, err
	}
	defer uaRows.Close()
	var uaList []map[string]any
	for uaRows.Next() {
		var ua string
		var c int64
		if err := uaRows.Scan(&ua, &c); err != nil {
			return types.IpSummary{}, err
		}
		uaList = append(uaList, map[string]any{"ua": ua, "count": c})
	}
	// Top paths
	pathRows, err := dbConn.QueryContext(ctx, `
        SELECT r.path, COUNT(*)
        FROM request r
        WHERE r.ip = $1 AND r.ts >= $2
        GROUP BY r.path
        ORDER BY COUNT(*) DESC
        LIMIT 20`, ip, since)
	if err != nil {
		return types.IpSummary{}, err
	}
	defer pathRows.Close()
	var paths []map[string]any
	for pathRows.Next() {
		var p string
		var c int64
		if err := pathRows.Scan(&p, &c); err != nil {
			return types.IpSummary{}, err
		}
		paths = append(paths, map[string]any{"path": p, "count": c})
	}
	// Timeline
	tlRows, err := dbConn.QueryContext(ctx, `
        SELECT date_trunc($1, ts) AS bucket, COUNT(*)
        FROM request
        WHERE ip = $2 AND ts >= $3
        GROUP BY bucket
        ORDER BY bucket`, bucket, ip, since)
	if err != nil {
		return types.IpSummary{}, err
	}
	defer tlRows.Close()
	var timeline []types.TimelinePoint
	for tlRows.Next() {
		var t time.Time
		var c int64
		if err := tlRows.Scan(&t, &c); err != nil {
			return types.IpSummary{}, err
		}
		timeline = append(timeline, types.TimelinePoint{T: t, C: c})
	}
	classes, _ := buildClassCounts(ctx, ip, since)
	return types.IpSummary{UA: uaList, Paths: paths, Timeline: timeline, Classes: classes}, nil
}

// ---- Classification helpers ----
// classifyRequestLight derives a human-readable class label for a single request row
// using lightweight heuristics based on path/status/query and Sec-Fetch headers when available.
func classifyRequestLight(method, path, query string, status int, sfu, sfm, sfd, sfs string) string {
	m := strings.ToUpper(strings.TrimSpace(method))
	// Header-based click: top-level navigation initiated by a user gesture in Chromium
	if (m == "GET" || m == "POST") && sfu == "?1" && sfm == "navigate" && sfd == "document" {
		return "Real-User Click"
	}
	// JS beacon click (Safari/WebViews)
	if strings.HasPrefix(path, "/alpha/js") && status == 204 && strings.Contains(strings.ToLower(query), "evt=click") {
		return "Real-User Click"
	}
	// JS beacon any (boot/visible/challenge) means a real browser executed JS
	if strings.HasPrefix(path, "/alpha/js") && status == 204 {
		return "Real-User-Preview"
	}
	// Scanner-ish traffic
	if path == "/" || path == "/static/favicon.svg" || (path == "/alpha/pixel" && status == 202) {
		return "Scanner"
	}
	return ""
}

func buildClassCounts(ctx context.Context, ip string, since time.Time) (*types.ClassCounts, error) {
	if dbConn == nil {
		return nil, nil
	}
	out := &types.ClassCounts{}
	// preview-bot by UA patterns
	err := dbConn.QueryRowContext(ctx, `
        SELECT COUNT(*) FROM request r LEFT JOIN user_agent u ON u.id=r.ua_id
        WHERE r.ip=$1 AND r.ts >= $2 AND (
            u.ua ILIKE '%WhatsApp%' OR u.ua ILIKE '%Telegram%' OR u.ua ILIKE '%bot%' OR u.ua ILIKE '%crawler%'
            OR u.ua ILIKE '%facebookexternalhit%' OR u.ua ILIKE '%Slackbot%' OR u.ua ILIKE '%Twitterbot%'
            OR u.ua ILIKE '%LinkedInBot%' OR u.ua ILIKE '%Discordbot%' OR u.ua ILIKE '%SkypeUriPreview%'
            OR u.ua ILIKE '%Pinterestbot%'
        )`, ip, since).Scan(&out.PreviewBot)
	if err != nil {
		return out, err
	}
	// real-user-preview: JS beacons ok (status 204)
	_ = dbConn.QueryRowContext(ctx, `
        SELECT COUNT(*) FROM request r
        WHERE r.ip=$1 AND r.ts >= $2 AND r.path='/alpha/js' AND r.status=204
    `, ip, since).Scan(&out.RealUserPreview)
	// click-user: EITHER header-based click on top-level nav OR JS beacon click
	_ = dbConn.QueryRowContext(ctx, `
        SELECT COALESCE(SUM(c),0) FROM (
            SELECT COUNT(*) AS c FROM request r
            WHERE r.ip=$1 AND r.ts >= $2 AND r.method IN ('GET','POST')
              AND r.sfu='?1' AND r.sfm='navigate' AND r.sfd='document'
            UNION ALL
            SELECT COUNT(*) FROM request r
            WHERE r.ip=$1 AND r.ts >= $2 AND r.path='/alpha/js' AND r.status=204 AND r.query ILIKE '%evt=click%'
        ) x
    `, ip, since).Scan(&out.ClickUser)
	// scanner: root hits and bad pixel
	_ = dbConn.QueryRowContext(ctx, `
        SELECT COALESCE(SUM(c),0) FROM (
            SELECT COUNT(*) AS c FROM request r WHERE r.ip=$1 AND r.ts >= $2 AND r.path='/'
            UNION ALL
            SELECT COUNT(*) FROM request r WHERE r.ip=$1 AND r.ts >= $2 AND r.path='/alpha/pixel' AND r.status=202
        ) x
    `, ip, since).Scan(&out.Scanner)
	return out, nil
}

func handleIPExport(w http.ResponseWriter, r *http.Request) {
	if dbConn == nil {
		http.Error(w, "db disabled", http.StatusServiceUnavailable)
		return
	}
	ip, ok := extractIPFromPath(r.URL.Path, "/api/ip/", "/export")
	if !ok {
		http.NotFound(w, r)
		return
	}
	cat := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("cat")))
	fmtq := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("fmt")))
	if cat == "" || fmtq == "" {
		http.Error(w, "missing cat or fmt", http.StatusBadRequest)
		return
	}
	if fmtq != "json" && fmtq != "pdf" {
		http.Error(w, "fmt must be json or pdf", http.StatusBadRequest)
		return
	}
	filename := fmt.Sprintf("%s-%s.%s", strings.ReplaceAll(ip, ":", "_"), cat, fmtq)
	// Show PDFs inline in the browser; JSON stays as download
	if fmtq == "pdf" {
		w.Header().Set("Content-Disposition", "inline; filename="+filename)
	} else {
		w.Header().Set("Content-Disposition", "attachment; filename="+filename)
	}
	switch fmtq {
	case "json":
		exportJSON(w, r, ip, cat)
	case "pdf":
		exportPDF(w, r, ip, cat)
	}
}

func exportJSON(w http.ResponseWriter, r *http.Request, ip, cat string) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	w.Header().Set("Content-Type", "application/json")
	switch cat {
	case "all":
		// Build a single JSON object: { ip, generated_at, geo, ua, summary, requests }
		w.Write([]byte("{"))
		// ip
		b, _ := json.Marshal(ip)
		w.Write([]byte("\"ip\":"))
		w.Write(b)
		// generated_at
		w.Write([]byte(",\"generated_at\":"))
		b, _ = json.Marshal(utils.NowUTC())
		w.Write(b)
		// geo
		w.Write([]byte(",\"geo\":"))
		var geoB []byte
		var fetched time.Time
		if err := dbConn.QueryRowContext(ctx, `SELECT json, fetched_at FROM ip_geo WHERE ip=$1`, ip).Scan(&geoB, &fetched); err == nil {
			w.Write(geoB)
		} else {
			w.Write([]byte("null"))
		}
		// ua
		w.Write([]byte(",\"ua\":"))
		{
			rows, err := dbConn.QueryContext(ctx, `
                SELECT COALESCE(u.ua,''), COUNT(*)
                FROM request r LEFT JOIN user_agent u ON u.id=r.ua_id
                WHERE r.ip=$1
                GROUP BY u.ua
                ORDER BY COUNT(*) DESC`, ip)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			defer rows.Close()
			type rec struct {
				UA    string `json:"ua"`
				Count int64  `json:"count"`
			}
			w.Write([]byte("["))
			first := true
			for rows.Next() {
				var u string
				var c int64
				if err := rows.Scan(&u, &c); err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				rb, _ := json.Marshal(rec{UA: u, Count: c})
				if !first {
					w.Write([]byte(","))
				}
				first = false
				w.Write(rb)
			}
			w.Write([]byte("]"))
		}
		// summary (last 30d, hourly)
		w.Write([]byte(",\"summary\":"))
		if sum, err := buildIPSummary(ctx, ip, "hour", 30*24*time.Hour); err == nil {
			sb, _ := json.Marshal(sum)
			w.Write(sb)
		} else {
			w.Write([]byte("null"))
		}
		// requests (default limit, can be overridden via ?limit=)
		w.Write([]byte(",\"requests\":"))
		{
			limit := clamp(parseIntDefault(r.URL.Query().Get("limit"), 100000), 1, 2000000)
			rows, err := dbConn.QueryContext(ctx, `
                SELECT r.ts, r.method, r.path, r.query, r.status, r.duration_ms, r.request_id, COALESCE(u.ua,''), COALESCE(r.sfu,''), COALESCE(r.sfm,''), COALESCE(r.sfd,''), COALESCE(r.sfs,'')
                FROM request r LEFT JOIN user_agent u ON u.id=r.ua_id
                WHERE r.ip = $1
                ORDER BY r.ts DESC
                LIMIT $2`, ip, limit)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			defer rows.Close()
			type row struct {
				Ts         time.Time `json:"ts"`
				Method     string    `json:"method"`
				Path       string    `json:"path"`
				Query      string    `json:"query"`
				Status     int       `json:"status"`
				DurationMs int64     `json:"duration_ms"`
				RequestID  string    `json:"request_id"`
				UA         string    `json:"ua"`
				Class      string    `json:"class,omitempty"`
			}
			w.Write([]byte("["))
			first := true
			for rows.Next() {
				var ts time.Time
				var method, path, query, reqID, ua string
				var status int
				var dur int64
				var sfu, sfm, sfd, sfs string
				if err := rows.Scan(&ts, &method, &path, &query, &status, &dur, &reqID, &ua, &sfu, &sfm, &sfd, &sfs); err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				cls := classifyRequestLight(method, path, query, status, sfu, sfm, sfd, sfs)
				x := row{Ts: ts, Method: method, Path: path, Query: query, Status: status, DurationMs: dur, RequestID: reqID, UA: ua, Class: cls}
				rb, _ := json.Marshal(x)
				if !first {
					w.Write([]byte(","))
				}
				first = false
				w.Write(rb)
			}
			w.Write([]byte("]"))
		}
		w.Write([]byte("}"))
		return
	case "geo":
		var b []byte
		var fetched time.Time
		err := dbConn.QueryRowContext(ctx, `SELECT json, fetched_at FROM ip_geo WHERE ip=$1`, ip).Scan(&b, &fetched)
		if err != nil {
			http.Error(w, "geo not found", http.StatusNotFound)
			return
		}
		w.Write(b)
	case "ua":
		// derive UA list (all-time for IP)
		rows, err := dbConn.QueryContext(ctx, `
            SELECT COALESCE(u.ua,''), COUNT(*)
            FROM request r LEFT JOIN user_agent u ON u.id=r.ua_id
            WHERE r.ip=$1
            GROUP BY u.ua
            ORDER BY COUNT(*) DESC`, ip)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		type rec struct {
			UA    string `json:"ua"`
			Count int64  `json:"count"`
		}
		w.Write([]byte("["))
		first := true
		for rows.Next() {
			var u string
			var c int64
			if err := rows.Scan(&u, &c); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			b, _ := json.Marshal(rec{UA: u, Count: c})
			if !first {
				w.Write([]byte(","))
			}
			first = false
			w.Write(b)
		}
		w.Write([]byte("]"))
	case "summary":
		sum, err := buildIPSummary(ctx, ip, "hour", 30*24*time.Hour)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(sum)
	case "requests":
		// stream rows; optional limit
		limit := clamp(parseIntDefault(r.URL.Query().Get("limit"), 100000), 1, 2000000)
		rows, err := dbConn.QueryContext(ctx, `
            SELECT r.ts, r.method, r.path, r.query, r.status, r.duration_ms, r.request_id, COALESCE(u.ua,''), COALESCE(r.sfu,''), COALESCE(r.sfm,''), COALESCE(r.sfd,''), COALESCE(r.sfs,'')
            FROM request r LEFT JOIN user_agent u ON u.id=r.ua_id
            WHERE r.ip = $1
            ORDER BY r.ts DESC
            LIMIT $2`, ip, limit)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		type row struct {
			Ts         time.Time `json:"ts"`
			Method     string    `json:"method"`
			Path       string    `json:"path"`
			Query      string    `json:"query"`
			Status     int       `json:"status"`
			DurationMs int64     `json:"duration_ms"`
			RequestID  string    `json:"request_id"`
			UA         string    `json:"ua"`
			Class      string    `json:"class,omitempty"`
		}
		w.Write([]byte("["))
		first := true
		for rows.Next() {
			var ts time.Time
			var method, path, query, reqID, ua string
			var status int
			var dur int64
			var sfu, sfm, sfd, sfs string
			if err := rows.Scan(&ts, &method, &path, &query, &status, &dur, &reqID, &ua, &sfu, &sfm, &sfd, &sfs); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			cls := classifyRequestLight(method, path, query, status, sfu, sfm, sfd, sfs)
			x := row{Ts: ts, Method: method, Path: path, Query: query, Status: status, DurationMs: dur, RequestID: reqID, UA: ua, Class: cls}
			b, _ := json.Marshal(x)
			if !first {
				w.Write([]byte(","))
			}
			first = false
			w.Write(b)
		}
		w.Write([]byte("]"))
	default:
		http.Error(w, "unknown category", http.StatusBadRequest)
	}
}

func exportPDF(w http.ResponseWriter, r *http.Request, ip, cat string) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	w.Header().Set("Content-Type", "application/pdf")
	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.SetMargins(12, 16, 12)
	// Unicode translator for cp1252, fixes characters like em-dash/umlauts
	tr := pdf.UnicodeTranslatorFromDescriptor("")
	// Header/Footer
	brand := "LinkPeek"
	pdf.SetHeaderFuncMode(func() {
		// Centered brand only (no timestamp)
		pdf.SetFont("Helvetica", "B", 12)
		pdf.CellFormat(0, 8, tr(brand), "", 1, "C", false, 0, "")
		// Place content start safely below header
		pdf.SetY(20)
	}, true)
	pdf.SetFooterFunc(func() {
		pdf.SetY(-12)
		pdf.SetFont("Helvetica", "", 9)
		pdf.CellFormat(0, 6, tr(fmt.Sprintf("Page %d", pdf.PageNo())), "T", 0, "R", false, 0, "")
	})
	// helpers
	writeSectionTitle := func(title string) {
		pdf.Ln(1)
		pdf.SetFont("Helvetica", "B", 14)
		pdf.CellFormat(0, 8, tr(title), "", 1, "L", false, 0, "")
		pdf.Ln(1)
	}
	writeMono := func(s string) {
		pdf.SetFont("Courier", "", 9)
		for _, line := range strings.Split(s, "\n") {
			pdf.MultiCell(0, 4.5, tr(line), "", "L", false)
		}
		pdf.Ln(1)
	}
	writeRequests := func(rows *sql.Rows) error {
		pdf.SetFont("Helvetica", "", 9)
		// headers
		pdf.SetFillColor(17, 23, 32)
		pdf.CellFormat(40, 6, tr("Time"), "B", 0, "L", false, 0, "")
		pdf.CellFormat(32, 6, tr("Class"), "B", 0, "L", false, 0, "")
		pdf.CellFormat(16, 6, tr("Method"), "B", 0, "L", false, 0, "")
		pdf.CellFormat(16, 6, tr("Status"), "B", 0, "L", false, 0, "")
		pdf.CellFormat(0, 6, tr("Path"), "B", 1, "L", false, 0, "")
		type row struct {
			Ts     time.Time
			Method string
			Path   string
			Query  string
			Status int
		}
		for rows.Next() {
			var x row
			if err := rows.Scan(&x.Ts, &x.Method, &x.Path, &x.Query, &x.Status); err != nil {
				return err
			}
			t := x.Ts.Format("2006-01-02 15:04:05")
			path := x.Path
			if x.Query != "" {
				path += "?" + x.Query
			}
			// classify for PDF (lightweight heuristic)
			cls := classifyRequestLight(x.Method, x.Path, x.Query, x.Status, "", "", "", "")
			pdf.CellFormat(40, 5.5, tr(t), "", 0, "L", false, 0, "")
			pdf.CellFormat(32, 5.5, tr(cls), "", 0, "L", false, 0, "")
			pdf.CellFormat(16, 5.5, tr(strings.ToUpper(x.Method)), "", 0, "L", false, 0, "")
			pdf.CellFormat(16, 5.5, tr(strconv.Itoa(x.Status)), "", 0, "L", false, 0, "")
			pdf.MultiCell(0, 5.5, tr(path), "", "L", false)
		}
		return nil
	}

	pdf.AddPage()
	// Title block in body: IP line with separator, then a small gap
	pdf.SetFont("Helvetica", "", 10)
	pdf.CellFormat(0, 6, tr("IP "+ip), "B", 1, "L", false, 0, "")
	pdf.Ln(1)

	switch cat {
	case "all":
		// Geo
		writeSectionTitle("Geo")
		{
			var b []byte
			var fetched time.Time
			if err := dbConn.QueryRowContext(ctx, `SELECT json, fetched_at FROM ip_geo WHERE ip=$1`, ip).Scan(&b, &fetched); err == nil {
				writeMono(string(b))
			} else {
				writeMono("no geo data")
			}
		}
		// Summary
		writeSectionTitle("Summary (last 30 days, hourly)")
		if sum, err := buildIPSummary(ctx, ip, "hour", 30*24*time.Hour); err == nil {
			sb, _ := json.MarshalIndent(sum, "", "  ")
			writeMono(string(sb))
		} else {
			writeMono("summary unavailable")
		}
		// User Agents
		writeSectionTitle("User Agents")
		if rows, err := dbConn.QueryContext(ctx, `
            SELECT COALESCE(u.ua,''), COUNT(*)
            FROM request r LEFT JOIN user_agent u ON u.id=r.ua_id
            WHERE r.ip=$1
            GROUP BY u.ua
            ORDER BY COUNT(*) DESC`, ip); err == nil {
			defer rows.Close()
			for rows.Next() {
				var u string
				var c int64
				_ = rows.Scan(&u, &c)
				writeMono(fmt.Sprintf("%6d  %s", c, u))
			}
		} else {
			writeMono("user agents unavailable")
		}
		// Requests (limit parameter)
		writeSectionTitle("Requests")
		{
			limit := clamp(parseIntDefault(r.URL.Query().Get("limit"), 1000), 1, 200000)
			if rows, err := dbConn.QueryContext(ctx, `
                SELECT r.ts, r.method, r.path, r.query, r.status
                FROM request r
                WHERE r.ip=$1
                ORDER BY r.ts DESC
                LIMIT $2`, ip, limit); err == nil {
				defer rows.Close()
				_ = writeRequests(rows)
			} else {
				writeMono("requests unavailable")
			}
		}
		_ = pdf.Output(w)
		return
	case "geo":
		writeSectionTitle("Geo")
		var b []byte
		var fetched time.Time
		err := dbConn.QueryRowContext(ctx, `SELECT json, fetched_at FROM ip_geo WHERE ip=$1`, ip).Scan(&b, &fetched)
		if err != nil {
			http.Error(w, "geo not found", http.StatusNotFound)
			return
		}
		writeMono(string(b))
	case "ua":
		writeSectionTitle("User Agents")
		rows, err := dbConn.QueryContext(ctx, `
            SELECT COALESCE(u.ua,''), COUNT(*)
            FROM request r LEFT JOIN user_agent u ON u.id=r.ua_id
            WHERE r.ip=$1
            GROUP BY u.ua
            ORDER BY COUNT(*) DESC`, ip)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		for rows.Next() {
			var u string
			var c int64
			if err := rows.Scan(&u, &c); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			writeMono(fmt.Sprintf("%6d  %s", c, u))
		}
	case "summary":
		writeSectionTitle("Summary (last 30 days, hourly)")
		sum, err := buildIPSummary(ctx, ip, "hour", 30*24*time.Hour)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		b, _ := json.MarshalIndent(sum, "", "  ")
		writeMono(string(b))
	case "requests":
		writeSectionTitle("Requests")
		limit := clamp(parseIntDefault(r.URL.Query().Get("limit"), 1000), 1, 200000)
		rows, err := dbConn.QueryContext(ctx, `
            SELECT r.ts, r.method, r.path, r.query, r.status
            FROM request r
            WHERE r.ip=$1
            ORDER BY r.ts DESC
            LIMIT $2`, ip, limit)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		_ = writeRequests(rows)
	default:
		http.Error(w, "unknown category", http.StatusBadRequest)
		return
	}
	_ = pdf.Output(w)
}

func handleIPDelete(w http.ResponseWriter, r *http.Request) {
	if dbConn == nil {
		http.Error(w, "db disabled", http.StatusServiceUnavailable)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ip, ok := extractIPFromPath(r.URL.Path, "/api/ip/", "/delete")
	if !ok {
		http.NotFound(w, r)
		return
	}
	cat := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("cat")))
	if cat == "" {
		http.Error(w, "missing cat", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	switch cat {
	case "geo":
		_, _ = dbConn.ExecContext(ctx, `DELETE FROM ip_geo WHERE ip=$1`, ip)
	case "requests", "ua", "summary":
		_, _ = dbConn.ExecContext(ctx, `DELETE FROM request WHERE ip=$1`, ip)
		// recompute ip_profile row
		var cnt int64
		_ = dbConn.QueryRowContext(ctx, `SELECT COUNT(*) FROM request WHERE ip=$1`, ip).Scan(&cnt)
		if cnt == 0 {
			_, _ = dbConn.ExecContext(ctx, `DELETE FROM ip_profile WHERE ip=$1`, ip)
		} else {
			var first, last time.Time
			_ = dbConn.QueryRowContext(ctx, `SELECT MIN(ts), MAX(ts) FROM request WHERE ip=$1`, ip).Scan(&first, &last)
			_, _ = dbConn.ExecContext(ctx, `UPDATE ip_profile SET first_seen=$2, last_seen=$3, req_count=$4 WHERE ip=$1`, ip, first, last, cnt)
		}
	case "all":
		// Delete everything for this IP: geo, requests, and profile entry
		_, _ = dbConn.ExecContext(ctx, `DELETE FROM ip_geo WHERE ip=$1`, ip)
		_, _ = dbConn.ExecContext(ctx, `DELETE FROM request WHERE ip=$1`, ip)
		_, _ = dbConn.ExecContext(ctx, `DELETE FROM ip_profile WHERE ip=$1`, ip)
	default:
		http.Error(w, "unknown category", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

func extractIPFromPath(path, prefix, suffix string) (string, bool) {
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return "", false
	}
	s := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	if s == "" {
		return "", false
	}
	// Normalize IPv6 brackets if any (should not be present in stored values)
	s = strings.Trim(s, "[]")
	return s, true
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

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func parseDurationHuman(s string) (time.Duration, error) {
	// Accept Go-style durations (e.g., 24h, 7h30m) or simple days like 7d
	if strings.HasSuffix(s, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err != nil {
			return 0, err
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}

// --- DB-IP integration ---
// Configure via env: DBIP_API_KEY (optional). If absent, we'll skip network calls and only use cache.
// TTL is 7 days by default.

func handleIPGeo(w http.ResponseWriter, r *http.Request) {
	if dbConn == nil {
		http.Error(w, "db disabled", http.StatusServiceUnavailable)
		return
	}
	ip, ok := extractIPFromPath(r.URL.Path, "/api/ip/", "/geo")
	if !ok {
		http.NotFound(w, r)
		return
	}
	ttl := 7 * 24 * time.Hour
	if s := strings.TrimSpace(r.URL.Query().Get("ttl")); s != "" {
		if d, err := parseDurationHuman(s); err == nil {
			ttl = d
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()

	// try cache
	var b []byte
	var fetched time.Time
	err := dbConn.QueryRowContext(ctx, `SELECT json, fetched_at FROM ip_geo WHERE ip=$1`, ip).Scan(&b, &fetched)
	if err == nil && time.Since(fetched) < ttl {
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
		return
	}

	// fetch from DB-IP if key present
	apiKey := strings.TrimSpace(os.Getenv("DBIP_API_KEY"))
	if apiKey == "" {
		if err == nil {
			// return stale cached data if available
			w.Header().Set("Content-Type", "application/json")
			w.Write(b)
			return
		}
		http.Error(w, "DB-IP disabled (no API key)", http.StatusServiceUnavailable)
		return
	}
	// call remote API
	url := fmt.Sprintf("https://api.db-ip.com/v2/%s/%s", apiKey, ip)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	req.Header.Set("Accept", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
		return
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	// upsert cache
	_, _ = dbConn.ExecContext(ctx, `
        INSERT INTO ip_geo(ip, json, fetched_at) VALUES($1,$2,$3)
        ON CONFLICT (ip) DO UPDATE SET json=EXCLUDED.json, fetched_at=EXCLUDED.fetched_at
    `, ip, body, utils.NowUTC())

	w.Header().Set("Content-Type", "application/json")
	w.Write(body)
}

// prefetchGeoIfAbsent fetches and stores IP geo data once per IP, using DB-IP.
// It prefers the API key endpoint if DBIP_API_KEY is set, otherwise falls back to the free endpoint.
// Supports IPv4 and IPv6. Best-effort; failures are logged softly and ignored.
func prefetchGeoIfAbsent(ip string) {
	if dbConn == nil {
		return
	}
	// Validate IP format early
	if net.ParseIP(strings.TrimSpace(ip)) == nil {
		return
	}
	// Skip private IPs
	if isPrivateIP(ip) {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Check if already present
	var exists bool
	if err := dbConn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM ip_geo WHERE ip=$1)`, ip).Scan(&exists); err != nil {
		// soft log
		// log.Printf("ip_geo exists check error for %s: %v", ip, err)
		return
	}
	if exists {
		return
	}

	// Build URL
	apiKey := strings.TrimSpace(os.Getenv("DBIP_API_KEY"))
	var url string
	if apiKey != "" {
		url = fmt.Sprintf("https://api.db-ip.com/v2/%s/%s", apiKey, ip)
	} else {
		url = fmt.Sprintf("https://api.db-ip.com/v2/free/%s", ip)
	}

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	req.Header.Set("Accept", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return
	}
	// Validate JSON minimally
	var js map[string]any
	if err := json.Unmarshal(body, &js); err != nil {
		return
	}
	// Store once; ignore conflict
	_, _ = dbConn.ExecContext(ctx, `
        INSERT INTO ip_geo(ip, json, fetched_at) VALUES($1,$2,$3)
        ON CONFLICT (ip) DO NOTHING
    `, ip, body, utils.NowUTC())
}

// ---- Payload lab ----
func handlePayloads(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		list := listPayloads()
		items := make([]types.PayloadListItem, 0, len(list))
		for _, meta := range list {
			items = append(items, types.PayloadListItem{Payload: meta, Variants: payloadVariantsFor(meta.ID)})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"items": items})
	case http.MethodPost:
		if payloadDir == "" {
			http.Error(w, "payload storage unavailable", http.StatusServiceUnavailable)
			return
		}
		if payloadMaxUploadBytes > 0 {
			r.Body = http.MaxBytesReader(w, r.Body, payloadMaxUploadBytes)
		}
		if err := r.ParseMultipartForm(payloadMaxUploadBytes); err != nil {
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

		id := newPayloadID()
		dir := filepath.Join(payloadDir, id)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		dst, err := os.Create(filepath.Join(dir, payloadDataFilename))
		if err != nil {
			_ = os.RemoveAll(dir)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer dst.Close()

		sniffBuf := make([]byte, 512)
		n, readErr := file.Read(sniffBuf)
		if readErr != nil && readErr != io.EOF {
			_ = os.RemoveAll(dir)
			http.Error(w, readErr.Error(), http.StatusInternalServerError)
			return
		}
		contentType := header.Header.Get("Content-Type")
		if contentType == "" && n > 0 {
			contentType = http.DetectContentType(sniffBuf[:n])
		}
		if contentType == "" && header.Filename != "" {
			if mt := mime.TypeByExtension(strings.ToLower(filepath.Ext(header.Filename))); mt != "" {
				contentType = mt
			}
		}
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		if n > 0 {
			if _, err := dst.Write(sniffBuf[:n]); err != nil {
				_ = os.RemoveAll(dir)
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		size := int64(n)
		copied, err := io.Copy(dst, file)
		if err != nil {
			_ = os.RemoveAll(dir)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		size += copied
		now := utils.NowUTC()
		meta := &types.PayloadMeta{
			ID:               id,
			Name:             name,
			Category:         category,
			OriginalFilename: header.Filename,
			Size:             size,
			MimeType:         contentType,
			CreatedAt:        now,
			UpdatedAt:        now,
		}
		if err := savePayloadMeta(meta); err != nil {
			_ = os.RemoveAll(dir)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		setPayloadMeta(meta)

		// Publish payload list update
		if realtimeHub != nil {
			go publishPayloadList()
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(types.PayloadListItem{Payload: *meta, Variants: payloadVariantsFor(meta.ID)})
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func handlePayloadItem(w http.ResponseWriter, r *http.Request) {
	id, ok := extractPayloadIDFromPath(r.URL.Path, "/api/payloads/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	// Method validation is handled by middleware
	if _, exists := getPayloadMeta(id); !exists {
		http.NotFound(w, r)
		return
	}
	if err := os.RemoveAll(filepath.Join(payloadDir, id)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	deletePayloadMeta(id)

	// Publish payload list update
	if realtimeHub != nil {
		go publishPayloadList()
	}

	w.WriteHeader(http.StatusNoContent)
}

func handlePayloadVariant(kind string) http.HandlerFunc {
	prefix := "/payload/" + kind + "/"
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := extractPayloadIDFromPath(r.URL.Path, prefix)
		if !ok {
			http.NotFound(w, r)
			return
		}
		meta, ok := getPayloadMeta(id)
		if !ok {
			http.NotFound(w, r)
			return
		}
		switch kind {
		case "raw":
			servePayloadFile(w, r, meta, "", "")
		case "inline":
			servePayloadFile(w, r, meta, "", contentDisposition("inline", meta.OriginalFilename))
		case "download":
			servePayloadFile(w, r, meta, "", contentDisposition("attachment", meta.OriginalFilename))
		case "mime-mismatch":
			servePayloadFile(w, r, meta, mismatchMime(meta.MimeType), "")
		case "corrupt":
			servePayloadCorrupt(w, r, meta)
		case "slow":
			servePayloadSlow(w, r, meta, 500*time.Millisecond)
		case "chunked":
			servePayloadSlow(w, r, meta, 0)
		case "redirect":
			http.Redirect(w, r, fmt.Sprintf("/payload/inline/%s?via=redirect", meta.ID), http.StatusFound)
		case "error":
			servePayloadError(w, r, meta)
		case "range":
			servePayloadRange(w, r, meta)
		default:
			http.NotFound(w, r)
		}
	}
}

func handlePayloadSpectrum(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id, seedStr, ok := extractSpectrumParams(r.URL.Path, "/payload/spec/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	meta, ok := getPayloadMeta(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	seed, flags, etag := deriveSpectrumSeed(meta.ID, seedStr)
	if etag != "" && etagMatches(etag, r.Header.Get("If-None-Match")) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	data, err := generateSpectrumVariant(meta, seed)
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
		w.Header().Set("Content-Disposition", contentDisposition(disp, filename))
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

func handlePayloadSpectrumHTML(w http.ResponseWriter, r *http.Request) {
	serveSpectrumWrapper(w, r, "html", []string{"/payload/spec/html/", "/payload/spec-html/"})
}

func handlePayloadSpectrumOG(w http.ResponseWriter, r *http.Request) {
	serveSpectrumWrapper(w, r, "og", []string{"/payload/spec/og/", "/payload/spec-og/"})
}

func serveSpectrumWrapper(w http.ResponseWriter, r *http.Request, mode string, prefixes []string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
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
	meta, ok := getPayloadMeta(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	_, flags, _ := deriveSpectrumSeed(meta.ID, seedStr)
	// reuse flags to ensure stable MIME suggestions when query override missing
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
		w.Header().Set("Content-Disposition", contentDisposition("inline", filename))
	}
	imagePath := fmt.Sprintf("/payload/spec/%s/%s", neturl.PathEscape(id), neturl.PathEscape(seedStr))
	if q := strings.TrimSpace(r.URL.RawQuery); q != "" {
		imagePath = imagePath + "?" + q
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

func generateSpectrumVariant(meta *types.PayloadMeta, seed int64) ([]byte, error) {
	original, err := os.ReadFile(payloadDataPath(meta.ID))
	if err != nil {
		return nil, err
	}
	if len(original) == 0 {
		return original, nil
	}
	buf := make([]byte, len(original))
	copy(buf, original)
	rng := mathrand.New(mathrand.NewSource(seed))
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

func applySpectrumMutation(buf []byte, rng *mathrand.Rand) {
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

func servePayloadFile(w http.ResponseWriter, r *http.Request, meta *types.PayloadMeta, overrideType, disposition string) {
	f, info, err := openPayloadFile(meta)
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

func servePayloadCorrupt(w http.ResponseWriter, r *http.Request, meta *types.PayloadMeta) {
	f, err := os.Open(payloadDataPath(meta.ID))
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

func servePayloadSlow(w http.ResponseWriter, r *http.Request, meta *types.PayloadMeta, pause time.Duration) {
	f, err := os.Open(payloadDataPath(meta.ID))
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

func servePayloadError(w http.ResponseWriter, r *http.Request, meta *types.PayloadMeta) {
	apierror.WriteError(w, http.StatusInternalServerError, "payload_error", "simulated payload failure", map[string]string{"payload": meta.ID})
}

func servePayloadRange(w http.ResponseWriter, r *http.Request, meta *types.PayloadMeta) {
	f, info, err := openPayloadFile(meta)
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

func openPayloadFile(meta *types.PayloadMeta) (*os.File, os.FileInfo, error) {
	f, err := os.Open(payloadDataPath(meta.ID))
	if err != nil {
		return nil, nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	return f, info, nil
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

func contentDisposition(kind, name string) string {
	if name == "" {
		return kind
	}
	clean := strings.ReplaceAll(name, "\"", "")
	escaped := neturl.PathEscape(clean)
	return fmt.Sprintf("%s; filename=\"%s\"; filename*=UTF-8''%s", kind, clean, escaped)
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
		// suffix range
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

func extractPayloadIDFromPath(path, prefix string) (string, bool) {
	if !strings.HasPrefix(path, prefix) {
		return "", false
	}
	id := strings.TrimPrefix(path, prefix)
	if idx := strings.IndexRune(id, '/'); idx >= 0 {
		id = id[:idx]
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return "", false
	}
	return id, true
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

// ---- IP stats ----
func handleIPCount(w http.ResponseWriter, r *http.Request) {
	if dbConn == nil {
		http.Error(w, "db disabled", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	var cnt int64
	if err := dbConn.QueryRowContext(ctx, `SELECT COUNT(*) FROM ip_profile`).Scan(&cnt); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"count": cnt})
}

// ---- Process metrics ----
type metrics struct {
	CPUPercent float64 `json:"cpu_percent"`
	RSSBytes   int64   `json:"rss_bytes"`
	UptimeSec  int64   `json:"uptime_sec"`
}

func handleMetrics(w http.ResponseWriter, r *http.Request) {
	m := metrics{UptimeSec: int64(time.Since(startedAt).Seconds())}
	// Try to read process CPU time from /proc/self/stat (Linux inside container)
	if b, err := os.ReadFile("/proc/self/stat"); err == nil {
		// fields split by space; utime=14, stime=15 (1-based).
		// Need clock ticks per second to convert; on Linux it's usually 100.
		parts := strings.Fields(string(b))
		if len(parts) >= 17 {
			// parse utime+stime in ticks
			uticks, _ := strconv.ParseFloat(parts[13], 64)
			sticks, _ := strconv.ParseFloat(parts[14], 64)
			// HZ ticks per second
			hz := 100.0
			procSecs := (uticks + sticks) / hz
			cpuSampleMu.Lock()
			now := time.Now()
			if !lastWall.IsZero() && procSecs >= lastProcCPUSeconds {
				wall := now.Sub(lastWall).Seconds()
				if wall > 0 {
					m.CPUPercent = 100.0 * (procSecs - lastProcCPUSeconds) / wall
					if m.CPUPercent < 0 {
						m.CPUPercent = 0
					}
				}
			}
			lastProcCPUSeconds = procSecs
			lastWall = now
			cpuSampleMu.Unlock()
		}
	}
	// RSS from /proc/self/statm (field 2) * page size
	if b, err := os.ReadFile("/proc/self/statm"); err == nil {
		parts := strings.Fields(string(b))
		if len(parts) >= 2 {
			pages, _ := strconv.ParseInt(parts[1], 10, 64)
			// assume 4096 page size
			m.RSSBytes = pages * 4096
		}
	}
	if m.RSSBytes == 0 {
		// fallback to Go heap stats (not equal to RSS, but better than nothing)
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		m.RSSBytes = int64(ms.Alloc)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(m)
}

// validateTemplates checks that required template files exist at startup
func validateTemplates() error {
	indexPath := filepath.Join("templates", "index.html")
	if _, err := os.Stat(indexPath); err != nil {
		return fmt.Errorf("template validation failed: %w", err)
	}
	if _, err := os.Stat(filepath.Join("templates", "login.html")); err != nil {
		log.Printf("warning: optional login template missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join("templates", "access.html")); err != nil {
		return fmt.Errorf("template validation failed: %w", err)
	}
	log.Printf("template validation passed: %s", indexPath)
	return nil
}

func serveStatic(mux *http.ServeMux) {
	staticDir := filepath.Join("static")
	fs := http.FileServer(http.Dir(staticDir))
	mux.Handle("/static/", http.StripPrefix("/static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Add caching headers for static files
		w.Header().Set("Cache-Control", "public, max-age=86400") // 1 day
		w.Header().Set("Vary", "Accept-Encoding")
		fs.ServeHTTP(w, r)
	})))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		if isTunnelHost(r.Host) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, "LinkPeek dashboard cannot be accessed over the shared tunnel. Use your trusted origin.")
			return
		}
		http.ServeFile(w, r, filepath.Join("templates", "index.html"))
	})
	// Favicon: redirect to a small neutral SVG so browsers stop 404-ing
	mux.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/static/favicon.svg", http.StatusTemporaryRedirect)
	})
}

func handleLogin(w http.ResponseWriter, r *http.Request) {
	if isTunnelHost(r.Host) {
		http.Error(w, "Login is disabled over the shared tunnel.", http.StatusForbidden)
		return
	}
	switch r.Method {
	case http.MethodGet:
		destination := sanitizeNext(r.URL.Query().Get("next"))
		if destination == "" {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/?next=%s", neturl.QueryEscape(destination)), http.StatusSeeOther)
	case http.MethodPost:
		contentType := r.Header.Get("Content-Type")
		if strings.Contains(contentType, "application/json") {
			var body struct {
				Password string `json:"password"`
				Next     string `json:"next"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "invalid json", http.StatusBadRequest)
				return
			}
			next := sanitizeNext(body.Next)
			if !verifyPassword(body.Password) {
				if token := readSessionToken(r); token != "" {
					sessions.Delete(token)
				}
				clearSessionCookie(w)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "Invalid password"})
				return
			}
			mustChange := mustChangePassword()
			token, expires := sessions.Create()
			if token == "" {
				http.Error(w, "session unavailable", http.StatusInternalServerError)
				return
			}
			setSessionCookie(w, token, expires, isSecureRequest(r))
			if mustChange {
				next = "/access?must_change=1"
			}
			if next == "" {
				next = "/"
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"ok":         true,
				"redirect":   next,
				"expires":    expires.UTC(),
				"mustChange": mustChange,
			})
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid form", http.StatusBadRequest)
			return
		}
		next := sanitizeNext(r.FormValue("next"))
		pw := r.FormValue("password")
		if !verifyPassword(pw) {
			if token := readSessionToken(r); token != "" {
				sessions.Delete(token)
			}
			clearSessionCookie(w)
			http.Redirect(w, r, "/?login_error=1", http.StatusSeeOther)
			return
		}
		mustChange := mustChangePassword()
		if mustChange {
			next = "/access?must_change=1"
		}
		token, expires := sessions.Create()
		if token == "" {
			http.Error(w, "session unavailable", http.StatusInternalServerError)
			return
		}
		setSessionCookie(w, token, expires, isSecureRequest(r))
		if next == "" {
			next = "/"
		}
		http.Redirect(w, r, next, http.StatusSeeOther)
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if token := readSessionToken(r); token != "" {
		sessions.Delete(token)
	}
	clearSessionCookie(w)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	token := readSessionToken(r)
	if ok, expires := sessions.Validate(token); ok {
		setSessionCookie(w, token, expires, isSecureRequest(r))
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"ok":         true,
			"expires":    expires.UTC(),
			"mustChange": mustChangePassword(),
		})
		return
	}
	if token != "" {
		sessions.Delete(token)
	}
	clearSessionCookie(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	json.NewEncoder(w).Encode(map[string]any{"ok": false})
}

type accessPageData struct {
	MustChange   bool
	Requirements string
	FlashMessage string
	ErrorMessage string
	LoggedIn     bool
}

func newAccessPageData() accessPageData {
	return accessPageData{
		MustChange:   mustChangePassword(),
		Requirements: auth.PasswordRequirements(),
	}
}

func renderAccessPage(w http.ResponseWriter, data accessPageData) {
	tpl, err := template.ParseFiles(filepath.Join("templates", "access.html"))
	if err != nil {
		log.Printf("render access page: %v", err)
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	}
	if err := tpl.Execute(w, data); err != nil {
		log.Printf("execute access template: %v", err)
	}
}

func handleAuthChange(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		if authStore == nil {
			http.Error(w, "auth unavailable", http.StatusInternalServerError)
			return
		}
		token := readSessionToken(r)
		if ok, expires := sessions.Validate(token); !ok {
			if token != "" {
				sessions.Delete(token)
			}
			clearSessionCookie(w)
			respondChangeUnauthorized(w, r)
			return
		} else {
			setSessionCookie(w, token, expires, isSecureRequest(r))
		}
		contentType := r.Header.Get("Content-Type")
		isJSON := strings.Contains(contentType, "application/json")
		var current, next, confirm string
		if isJSON {
			var body struct {
				Current string `json:"current"`
				Next    string `json:"next"`
				Confirm string `json:"confirm"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				respondChangeError(w, r, http.StatusBadRequest, "Invalid JSON payload", true)
				return
			}
			current = strings.TrimSpace(body.Current)
			next = strings.TrimSpace(body.Next)
			confirm = strings.TrimSpace(body.Confirm)
		} else {
			if err := r.ParseForm(); err != nil {
				respondChangeError(w, r, http.StatusBadRequest, "Invalid form submission", false)
				return
			}
			current = strings.TrimSpace(r.FormValue("current"))
			next = strings.TrimSpace(r.FormValue("next"))
			confirm = strings.TrimSpace(r.FormValue("confirm"))
		}
		if current == "" || next == "" {
			respondChangeError(w, r, http.StatusBadRequest, "Current and new passwords are required", isJSON)
			return
		}
		if confirm != "" && confirm != next {
			respondChangeError(w, r, http.StatusBadRequest, "Passwords do not match", isJSON)
			return
		}
		if err := authStore.ChangePassword(current, next); err != nil {
			switch {
			case errors.Is(err, auth.ErrPasswordTooShort), errors.Is(err, auth.ErrPasswordNoSpecial), errors.Is(err, auth.ErrPasswordUnchanged):
				respondChangeError(w, r, http.StatusBadRequest, err.Error(), isJSON)
			default:
				if strings.Contains(err.Error(), "current password is incorrect") {
					respondChangeError(w, r, http.StatusUnauthorized, "Current password is incorrect", isJSON)
					return
				}
				log.Printf("auth change failed: %v", err)
				respondChangeError(w, r, http.StatusInternalServerError, "Failed to update password", isJSON)
			}
			return
		}
		if token != "" {
			sessions.Delete(token)
		}
		token, expires := sessions.Create()
		if token == "" {
			respondChangeError(w, r, http.StatusInternalServerError, "Session unavailable", isJSON)
			return
		}
		setSessionCookie(w, token, expires, isSecureRequest(r))
		if isJSON {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"ok":         true,
				"redirect":   "/access?changed=1",
				"mustChange": mustChangePassword(),
			})
			return
		}
		http.Redirect(w, r, "/access?changed=1", http.StatusSeeOther)
	default:
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func respondChangeUnauthorized(w http.ResponseWriter, r *http.Request) {
	acceptsJSON := strings.Contains(r.Header.Get("Content-Type"), "application/json") || strings.Contains(r.Header.Get("Accept"), "application/json")
	if acceptsJSON {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]any{
			"ok":    false,
			"error": "Login required",
		})
		return
	}
	http.Redirect(w, r, "/?next=/access", http.StatusSeeOther)
}

func respondChangeError(w http.ResponseWriter, r *http.Request, status int, message string, isJSON bool) {
	if isJSON {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]any{
			"ok":         false,
			"error":      message,
			"mustChange": mustChangePassword(),
		})
		return
	}
	data := newAccessPageData()
	data.ErrorMessage = message
	data.LoggedIn = true
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	}
	w.WriteHeader(status)
	renderAccessPage(w, data)
}

func handleAccessPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	data := newAccessPageData()
	token := readSessionToken(r)
	ok, expires := sessions.Validate(token)
	if !ok {
		if token != "" {
			sessions.Delete(token)
		}
		clearSessionCookie(w)
		http.Redirect(w, r, "/?next=/access", http.StatusSeeOther)
		return
	}
	setSessionCookie(w, token, expires, isSecureRequest(r))
	data.LoggedIn = true
	q := r.URL.Query()
	if q.Get("must_change") == "1" {
		data.MustChange = true
	}
	if q.Get("changed") == "1" {
		data.FlashMessage = "Password updated successfully."
	}
	if msg := strings.TrimSpace(q.Get("error")); msg != "" {
		data.ErrorMessage = msg
	}
	renderAccessPage(w, data)
}

func verifyPassword(pw string) bool {
	if authStore == nil {
		return false
	}
	return authStore.Verify(pw)
}

func mustChangePassword() bool {
	if authStore == nil {
		return true
	}
	return authStore.MustChange()
}

func isSecureRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	proto := strings.ToLower(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")))
	if proto == "https" {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Ssl")), "on") {
		return true
	}
	return false
}

func isTunnelHost(host string) bool {
	if allowTunnelAdmin {
		return false
	}
	if tunnelSvc != nil {
		return tunnelSvc.IsTunnelHost(host)
	}
	return strings.Contains(strings.ToLower(strings.TrimSpace(host)), "trycloudflare.com")
}

// Alpha endpoints
func handleAlpha(w http.ResponseWriter, r *http.Request) {
	// Tokenized landing page: set cookie, include pixel with token, and JS beacons
	// Optional passthrough params for attribution
	q := r.URL.Query()
	src := q.Get("src")
	camp := q.Get("c")

	// generate a short token
	tok := fmt.Sprintf("%x", time.Now().UnixNano())
	// cookie: HttpOnly so nur Pixel nutzt den Cookie; JS sendet Token im Query
	http.SetCookie(w, &http.Cookie{
		Name:     "lp",
		Value:    tok,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(10 * time.Minute),
		Secure:   strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") || strings.HasSuffix(strings.ToLower(r.Host), "trycloudflare.com"),
	})
	// Build HTML
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	// simple, inline script: boot + visibility + click signals via sendBeacon
	html := fmt.Sprintf(`<!doctype html>
<html>
<head>
    <meta charset="utf-8">
    <meta name="robots" content="noindex,nofollow">
    <title>Alpha</title>
    <style>body{font-family:ui-sans-serif,system-ui,-apple-system,Segoe UI,Roboto,Helvetica,Arial;padding:16px;color:#e6edf3;background:#0b0f14}</style>
    <script>
    (function(){
        const t = %q; const p = new URLSearchParams({t:t,src:%q,c:%q});
        // Load a challenge script that sets a JS-only cookie and echoes a value
        try {
           const s = document.createElement('script');
           s.src = '/alpha/challenge.js?'+p.toString();
           s.async = true; document.head.appendChild(s);
        } catch(e){}
        // Boot and visibility beacons
        try { navigator.sendBeacon('/alpha/js?evt=boot&'+p.toString()); } catch(e){}
        try {
            const vis = () => { if (document.visibilityState==='visible') { navigator.sendBeacon('/alpha/js?evt=visible&'+p.toString()); document.removeEventListener('visibilitychange', vis); } };
            document.addEventListener('visibilitychange', vis);
            // multiple interaction types (fires once)
            const once = (name, opts) => document.addEventListener(name, () => { try { navigator.sendBeacon('/alpha/js?evt=click&'+p.toString()); } catch(e){} }, { once:true, passive:true, ...opts });
            once('pointerdown'); once('pointerup');
            once('touchstart'); once('touchend');
            once('mousedown'); once('click');
            once('keydown'); once('focus');
            // pagehide sends a final preview signal
            window.addEventListener('pagehide', () => { try{ navigator.sendBeacon('/alpha/js?evt=pagehide&'+p.toString()); }catch(e){} }, { once:true });
        } catch(e){}
    })();
    </script>
    <link rel="preload" as="image" href="/alpha/pixel?t=%[1]s&src=%[2]s&c=%[3]s">
    <link rel="icon" href="/static/favicon.svg" type="image/svg+xml" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
    </head>
<body>
    <p>Alpha link active.</p>
    <img src="/alpha/pixel?t=%[1]s&src=%[2]s&c=%[3]s" alt="" width="1" height="1">
    <p style="opacity:.6;font-size:12px">Tokenised preview. You can close this tab.</p>
</body>
</html>`, tok, src, camp)
	fmt.Fprint(w, html)
}

var oneByOneGIF = []byte{
	0x47, 0x49, 0x46, 0x38, 0x39, 0x61, 0x01, 0x00, 0x01, 0x00, 0x80, 0x00, 0x00,
	0x00, 0x00, 0x00, 0xff, 0xff, 0xff, 0x21, 0xf9, 0x04, 0x01, 0x00, 0x00, 0x00,
	0x00, 0x2c, 0x00, 0x00, 0x00, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00, 0x02, 0x02,
	0x44, 0x01, 0x00, 0x3b,
}

func handleAlphaPixel(w http.ResponseWriter, r *http.Request) {
	// Validate cookie token vs query param
	want := r.URL.Query().Get("t")
	var have string
	if c, err := r.Cookie("lp"); err == nil {
		have = c.Value
	}
	ok := want != "" && have != "" && want == have
	// classification-friendly status: 200=ok, 202=bad
	if ok {
		w.WriteHeader(http.StatusOK)
	} else {
		w.WriteHeader(http.StatusAccepted)
	}
	w.Header().Set("Content-Type", "image/gif")
	w.Header().Set("Cache-Control", "no-store, max-age=0")
	w.Write(oneByOneGIF)
}

// Alpha JS beacon endpoint. Accepts GET or POST with query param t (token) and evt (boot|visible|click).
func handleAlphaJS(w http.ResponseWriter, r *http.Request) {
	want := r.URL.Query().Get("t")
	evt := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("evt")))
	if evt == "" {
		evt = "boot"
	}
	var have string
	if c, err := r.Cookie("lp"); err == nil {
		have = c.Value
	}
	ok := want != "" && have != "" && want == have
	if ok {
		w.WriteHeader(http.StatusNoContent) // 204 -> js_ok
	} else {
		w.WriteHeader(http.StatusAccepted) // 202 -> js_bad
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
}

// alpha challenge: returns small JS that writes a JS-only cookie and pings /alpha/js?evt=challenge
func handleAlphaChallengeJS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	// We intentionally set a non-HttpOnly cookie (jsc) via JS, bots that don't execute JS won't set it.
	// Then we beacon challenge event.
	w.WriteHeader(http.StatusOK)
	t := r.URL.Query().Get("t")
	// 5-minute expiry
	fmt.Fprintf(w, "document.cookie='jsc=1; Max-Age=300; Path=/' ; try{ navigator.sendBeacon('/alpha/js?evt=challenge&t=%s'); }catch(e){};", templateJSString(t))
}

// templateJSString minimally escapes a token for inline JS string literal usage
func templateJSString(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "'", "\\'")
	return s
}

func Run(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

	cfg := config.Load()
	if cfg.SessionTTL <= 0 {
		cfg.SessionTTL = 12 * time.Hour
	}

	dataDir = cfg.DataDir
	allowTunnelAdmin = cfg.AllowTunnelAdmin
	cloudflaredContainer = cfg.CloudflaredContainer
	payloadMaxUploadBytes = cfg.PayloadMaxUploadMB << 20 // Convert MB to bytes
	httpClient = &http.Client{Timeout: cfg.HTTPClientTimeout}
	loginRateLimiter = middleware.NewRateLimiter(cfg.RateLimitRequests, cfg.RateLimitWindow)
	realtimeEnabled = cfg.RealtimeEnabled

	if err := ensureDataDir(); err != nil {
		return err
	}

	var err error
	authStore, err = auth.NewStore(filepath.Join(dataDir, "auth.json"))
	if err != nil {
		return fmt.Errorf("failed to initialise auth store: %w", err)
	}
	if authStore.MustChange() {
		log.Printf("auth: default credentials active; password change required on first login")
	}

	sessions = newSessionManager(cfg.SessionTTL)
	dbInit()
	loadPayloadIndex()
	captureDir := filepath.Join(dataDir, "capture")
	captureManager, err = capture.NewManager(captureDir)
	if err != nil {
		return fmt.Errorf("failed to initialise capture manager: %w", err)
	}

	if mgr, mgrErr := domainpayload.NewManager(dataDir, payloadMaxUploadBytes, nil); mgrErr != nil {
		log.Printf("payload: failed to initialise manager: %v", mgrErr)
		payloadSvc = nil
	} else {
		payloadSvc = domainpayload.NewService(mgr)
	}

	var captureService *domaincapture.Service
	if captureManager != nil {
		captureService = domaincapture.NewService(captureManager)
	}

	snippetManager := domainsnippet.NewManager(cfg.SnippetMaxBytes)
	authService := domainauth.NewService(authStore, sessions)
	retryLabSvc = domainretry.NewLab(nil)

	tunnelSvc = domaintunnel.NewService(dataDir, cloudflaredContainer)
	if tunnelSvc != nil {
		tunnelSvc.LoadHistory()
	}

	if svc, svcErr := domainscanner.NewService(domainscanner.Config{
		DataDir:    dataDir,
		HTTPClient: httpClient,
		Logger:     log.Default(),
	}); svcErr != nil {
		log.Printf("scanner: failed to initialise service: %v", svcErr)
		scannerSvc = nil
	} else {
		scannerSvc = svc
	}

	if err := validateTemplates(); err != nil {
		return err
	}

	if realtimeEnabled {
		realtimeHub = realtime.NewHub()
		go realtimeHub.Start()
		log.Printf("realtime: hub started")

		if tunnelSvc != nil {
			tunnelSvc.SetPublishers(func(st domaintunnel.Status) {
				realtimeHub.Publish("tunnel.status", st)
			}, func(history []domaintunnel.HistoryItem) {
				realtimeHub.Publish("tunnel.history", history)
			})
			realtimeHub.RegisterSnapshotProvider("tunnel.status", realtime.SnapshotFunc(func() (interface{}, error) {
				tunnelSvc.RefreshStatus()
				return tunnelSvc.GetStatus(), nil
			}))
			realtimeHub.RegisterSnapshotProvider("tunnel.history", realtime.SnapshotFunc(func() (interface{}, error) {
				return tunnelSvc.GetHistory(), nil
			}))
		} else {
			realtimeHub.RegisterSnapshotProvider("tunnel.status", realtime.SnapshotFunc(func() (interface{}, error) {
				return domaintunnel.Status{}, nil
			}))
			realtimeHub.RegisterSnapshotProvider("tunnel.history", realtime.SnapshotFunc(func() (interface{}, error) {
				return []domaintunnel.HistoryItem{}, nil
			}))
		}
		if retryLabSvc != nil {
			retryLabSvc.SetPublisher(func() {
				realtimeHub.Publish("retry.stats", retryLabSvc.SnapshotStats())
			})
			realtimeHub.RegisterSnapshotProvider("retry.stats", realtime.SnapshotFunc(func() (interface{}, error) {
				return retryLabSvc.SnapshotStats(), nil
			}))
		} else {
			realtimeHub.RegisterSnapshotProvider("retry.stats", realtime.SnapshotFunc(func() (interface{}, error) {
				return []domainretry.StatDTO{}, nil
			}))
		}
		if captureService != nil {
			captureService.SetRealtimeHub(realtimeHub)
			realtimeHub.RegisterSnapshotProvider("capture.hooks", realtime.SnapshotFunc(func() (interface{}, error) {
				return captureService.ListHooks(), nil
			}))
			realtimeHub.RegisterSnapshotProvider("capture.activity", realtime.SnapshotFunc(func() (interface{}, error) {
				return captureService.RecentRequests(50), nil
			}))
		} else {
			realtimeHub.RegisterSnapshotProvider("capture.hooks", realtime.SnapshotFunc(func() (interface{}, error) {
				return []capture.Hook{}, nil
			}))
			realtimeHub.RegisterSnapshotProvider("capture.activity", realtime.SnapshotFunc(func() (interface{}, error) {
				return []capture.HookRequest{}, nil
			}))
		}

		if payloadSvc != nil {
			payloadSvc.SetRealtimeHub(realtimeHub)
			realtimeHub.RegisterSnapshotProvider("payload.list", realtime.SnapshotFunc(func() (interface{}, error) {
				return payloadSvc.Snapshot(), nil
			}))
		} else {
			realtimeHub.RegisterSnapshotProvider("payload.list", realtime.SnapshotFunc(func() (interface{}, error) {
				return snapshotPayloadList(), nil
			}))
		}

		if scannerSvc != nil {
			scannerSvc.SetRealtimeHub(realtimeHub)
			realtimeHub.RegisterSnapshotProvider("scanner.jobs", realtime.SnapshotFunc(func() (interface{}, error) {
				return scannerSvc.SnapshotJobs(), nil
			}))
			realtimeHub.RegisterSnapshotProvider("scanner.results", realtime.SnapshotFunc(func() (interface{}, error) {
				return scannerSvc.SnapshotResults(100), nil
			}))
		} else {
			realtimeHub.RegisterSnapshotProvider("scanner.jobs", realtime.SnapshotFunc(func() (interface{}, error) {
				return []domainscanner.Job{}, nil
			}))
			realtimeHub.RegisterSnapshotProvider("scanner.results", realtime.SnapshotFunc(func() (interface{}, error) {
				return []domainscanner.Result{}, nil
			}))
		}

		realtimeHub.RegisterSnapshotProvider("log.event", realtime.SnapshotFunc(func() (interface{}, error) {
			return recent.Last(50), nil
		}))

		if tunnelSvc != nil {
			tunnelSvc.RefreshStatus()
			go func() {
				ticker := time.NewTicker(5 * time.Second)
				defer ticker.Stop()
				for range ticker.C {
					tunnelSvc.RefreshStatus()
				}
			}()
		}
	}

	if !realtimeEnabled && payloadSvc != nil {
		payloadSvc.PublishList()
	}

	rt, err := appruntime.New(appruntime.Dependencies{
		Config:          cfg,
		Logger:          log.Default(),
		DataDir:         dataDir,
		PayloadDir:      payloadDir,
		DB:              dbConn,
		AuthStore:       authStore,
		Sessions:        sessions,
		AuthService:     authService,
		CaptureService:  captureService,
		PayloadService:  payloadSvc,
		RetryLab:        retryLabSvc,
		ScannerService:  scannerSvc,
		SnippetManager:  snippetManager,
		TunnelService:   tunnelSvc,
		LoginLimiter:    loginRateLimiter,
		HTTPClient:      httpClient,
		RealtimeHub:     realtimeHub,
		ShutdownTimeout: 10 * time.Second,
	})
	if err != nil {
		return fmt.Errorf("failed to construct application runtime: %w", err)
	}

	handler, err := router.New(router.Config{
		Runtime: rt,
		BuildRoutes: func(mux *http.ServeMux) error {
			registerRoutes(rt, mux)
			return nil
		},
		Middlewares: []router.Middleware{
			withGzip,
			withSecurityHeaders,
			withLogging,
			withAuth,
		},
	})
	if err != nil {
		return fmt.Errorf("failed to initialise router: %w", err)
	}

	addr := ":" + cfg.Port
	srv := &http.Server{Addr: addr, Handler: handler}
	errCh := make(chan error, 1)
	go func() {
		log.Printf("starting server on %s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(stop)

	select {
	case <-ctx.Done():
		log.Printf("context cancelled, shutting down: %v", ctx.Err())
	case sig := <-stop:
		log.Printf("received signal %s, shutting down", sig)
	case err := <-errCh:
		return err
	}

	timeout := rt.ShutdownTimeout()
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("graceful shutdown error: %v", err)
	}
	if scannerSvc != nil {
		scannerSvc.Shutdown()
	}
	if rt != nil {
		rtCtx, rtCancel := context.WithTimeout(context.Background(), timeout)
		rt.Shutdown(rtCtx)
		rtCancel()
	}
	if realtimeHub != nil {
		realtimeHub.Stop()
		log.Printf("realtime: hub stopped")
	}
	if sessions != nil {
		sessions.Close()
	}

	if err := <-errCh; err != nil {
		return err
	}
	return nil
}
