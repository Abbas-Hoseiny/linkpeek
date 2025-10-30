package capture

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	hooksFilename      = "hooks.json"
	requestsDirname    = "hooks"
	defaultBufferSize  = 200
	maxPreviewBytes    = 64 << 10  // 64 KiB preview per request
	maxStoredBodyBytes = 512 << 10 // hard cap per captured body
)

// Manager orchestrates capture hook persistence and in-memory caches.
type Manager struct {
	mu          sync.RWMutex
	hooks       map[string]*Hook
	tokenIndex  map[string]string
	buffers     map[string]*requestBuffer
	recent      *requestBuffer
	baseDir     string
	hooksPath   string
	requestsDir string
	bufferSize  int
}

// Hook represents a single externally shareable capture endpoint.
type Hook struct {
	ID            string    `json:"id"`
	Token         string    `json:"token"`
	Label         string    `json:"label"`
	Description   string    `json:"description,omitempty"`
	Secret        string    `json:"secret,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	LastRequest   time.Time `json:"last_request,omitempty"`
	TotalRequests int64     `json:"total_requests"`
}

// HookRequest captures the metadata of a single inbound HTTP request.
type HookRequest struct {
	ID            string            `json:"id"`
	HookID        string            `json:"hook_id"`
	HookLabel     string            `json:"hook_label,omitempty"`
	Method        string            `json:"method"`
	Host          string            `json:"host,omitempty"`
	Path          string            `json:"path"`
	Query         string            `json:"query,omitempty"`
	Headers       map[string]string `json:"headers,omitempty"`
	RemoteIP      string            `json:"remote_ip,omitempty"`
	UserAgent     string            `json:"user_agent,omitempty"`
	BodyPreview   string            `json:"body_preview,omitempty"`
	BodyEncoding  string            `json:"body_encoding,omitempty"`
	BodySize      int64             `json:"body_size,omitempty"`
	BodyTruncated bool              `json:"body_truncated,omitempty"`
	CreatedAt     time.Time         `json:"created_at"`
}

// requestBuffer keeps the last N requests in memory.
type requestBuffer struct {
	max   int
	items []HookRequest
}

func newRequestBuffer(max int) *requestBuffer {
	if max <= 0 {
		max = defaultBufferSize
	}
	return &requestBuffer{max: max, items: make([]HookRequest, 0, max)}
}

func (b *requestBuffer) add(req HookRequest) {
	if b == nil {
		return
	}
	if len(b.items) == b.max {
		copy(b.items[0:], b.items[1:])
		b.items[b.max-1] = req
		return
	}
	b.items = append(b.items, req)
}

func (b *requestBuffer) last(n int) []HookRequest {
	if b == nil || len(b.items) == 0 {
		return nil
	}
	if n <= 0 || n > len(b.items) {
		n = len(b.items)
	}
	out := make([]HookRequest, n)
	copy(out, b.items[len(b.items)-n:])
	return out
}

// NewManager initialises a capture manager rooted at dir (usually DATA_DIR).
func NewManager(dir string) (*Manager, error) {
	if dir == "" {
		return nil, errors.New("capture manager requires data dir")
	}
	hooksPath := filepath.Join(dir, hooksFilename)
	requestsDir := filepath.Join(dir, requestsDirname)
	if err := os.MkdirAll(requestsDir, 0o755); err != nil {
		return nil, err
	}
	m := &Manager{
		hooks:       map[string]*Hook{},
		tokenIndex:  map[string]string{},
		buffers:     map[string]*requestBuffer{},
		recent:      newRequestBuffer(defaultBufferSize),
		baseDir:     dir,
		hooksPath:   hooksPath,
		requestsDir: requestsDir,
		bufferSize:  defaultBufferSize,
	}
	if err := m.loadHooks(); err != nil {
		return nil, err
	}
	if err := m.warmBuffers(); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) loadHooks() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, err := os.ReadFile(m.hooksPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var list []Hook
	if err := json.Unmarshal(data, &list); err != nil {
		return err
	}
	m.hooks = make(map[string]*Hook, len(list))
	m.tokenIndex = make(map[string]string, len(list))
	for i := range list {
		hook := list[i]
		copyHook := hook
		m.hooks[hook.ID] = &copyHook
		m.tokenIndex[hook.Token] = hook.ID
	}
	return nil
}

func (m *Manager) persistHooksLocked() error {
	list := make([]Hook, 0, len(m.hooks))
	for _, hook := range m.hooks {
		list = append(list, *hook)
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].CreatedAt.Before(list[j].CreatedAt)
	})
	payload, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	tmp := m.hooksPath + ".tmp"
	if err := os.WriteFile(tmp, payload, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, m.hooksPath)
}

func (m *Manager) warmBuffers() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id := range m.hooks {
		if _, ok := m.buffers[id]; !ok {
			m.buffers[id] = newRequestBuffer(m.bufferSize)
		}
		if err := m.loadRecentRequestsLocked(id); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) loadRecentRequestsLocked(hookID string) error {
	path := m.requestsFile(hookID)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	const maxLine = maxStoredBodyBytes*2 + 4096
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, maxLine)
	temp := make([]HookRequest, 0, m.bufferSize)
	for scanner.Scan() {
		var req HookRequest
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			continue
		}
		temp = append(temp, req)
		if len(temp) > m.bufferSize {
			temp = temp[1:]
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	bufRef := m.buffers[hookID]
	bufRef.items = append(bufRef.items[:0], temp...)
	for _, req := range temp {
		m.recent.add(req)
	}
	return nil
}

// CreateHook registers a new capture endpoint.
func (m *Manager) CreateHook(label string) (Hook, error) {
	if label = strings.TrimSpace(label); label == "" {
		label = "Capture Link"
	}
	now := time.Now().UTC()
	hook := Hook{
		ID:        newHookID(),
		Token:     newToken(),
		Label:     label,
		CreatedAt: now,
		UpdatedAt: now,
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for {
		if _, exists := m.hooks[hook.ID]; !exists {
			break
		}
		hook.ID = newHookID()
	}
	for {
		if _, exists := m.tokenIndex[hook.Token]; !exists {
			break
		}
		hook.Token = newToken()
	}
	m.hooks[hook.ID] = &hook
	m.tokenIndex[hook.Token] = hook.ID
	m.buffers[hook.ID] = newRequestBuffer(m.bufferSize)
	if err := m.persistHooksLocked(); err != nil {
		delete(m.hooks, hook.ID)
		delete(m.tokenIndex, hook.Token)
		delete(m.buffers, hook.ID)
		return Hook{}, err
	}
	return hook, nil
}

// DeleteHook removes a hook and its stored requests.
func (m *Manager) DeleteHook(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	hook, ok := m.hooks[id]
	if !ok {
		return errors.New("hook not found")
	}
	delete(m.tokenIndex, hook.Token)
	delete(m.hooks, id)
	delete(m.buffers, id)
	if err := os.Remove(m.requestsFile(id)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return m.persistHooksLocked()
}

// ClearRequests deletes stored requests for a hook (without removing the hook).
func (m *Manager) ClearRequests(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.hooks[id]; !ok {
		return errors.New("hook not found")
	}
	delete(m.buffers, id)
	m.buffers[id] = newRequestBuffer(m.bufferSize)
	if err := os.Remove(m.requestsFile(id)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// ListHooks returns a snapshot of current hooks.
func (m *Manager) ListHooks() []Hook {
	m.mu.RLock()
	defer m.mu.RUnlock()
	list := make([]Hook, 0, len(m.hooks))
	for _, hook := range m.hooks {
		list = append(list, *hook)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].CreatedAt.Equal(list[j].CreatedAt) {
			return list[i].ID < list[j].ID
		}
		return list[i].CreatedAt.Before(list[j].CreatedAt)
	})
	return list
}

// GetHook retrieves a hook by ID.
func (m *Manager) GetHook(id string) (Hook, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	hook, ok := m.hooks[id]
	if !ok {
		return Hook{}, false
	}
	return *hook, true
}

// HookByToken resolves a hook via its token (used for inbound capture hits).
func (m *Manager) HookByToken(token string) (Hook, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	id, ok := m.tokenIndex[token]
	if !ok {
		return Hook{}, false
	}
	hook := m.hooks[id]
	if hook == nil {
		return Hook{}, false
	}
	return *hook, true
}

// RecordRequest appends a request for the given hook.
func (m *Manager) RecordRequest(hookID string, req HookRequest) (HookRequest, error) {
	req.ID = newRequestID()
	req.HookID = hookID
	req.CreatedAt = time.Now().UTC()

	m.mu.Lock()
	hook, ok := m.hooks[hookID]
	if !ok {
		m.mu.Unlock()
		return HookRequest{}, errors.New("hook not found")
	}
	req.HookLabel = hook.Label
	buf, ok := m.buffers[hookID]
	if !ok {
		buf = newRequestBuffer(m.bufferSize)
		m.buffers[hookID] = buf
	}
	buf.add(req)
	m.recent.add(req)
	hook.TotalRequests++
	hook.LastRequest = req.CreatedAt
	hook.UpdatedAt = req.CreatedAt
	if err := m.appendRequestLocked(hookID, req); err != nil {
		m.mu.Unlock()
		return HookRequest{}, err
	}
	if err := m.persistHooksLocked(); err != nil {
		m.mu.Unlock()
		return HookRequest{}, err
	}
	m.mu.Unlock()
	return req, nil
}

// ListRequests returns up to `limit` recent requests for a hook.
func (m *Manager) ListRequests(hookID string, limit int) []HookRequest {
	m.mu.RLock()
	defer m.mu.RUnlock()
	buf, ok := m.buffers[hookID]
	if !ok {
		return nil
	}
	items := buf.last(limit)
	copied := make([]HookRequest, len(items))
	copy(copied, items)
	return copied
}

// RecentRequests returns the most recent capture hits across hooks.
func (m *Manager) RecentRequests(limit int) []HookRequest {
	m.mu.RLock()
	defer m.mu.RUnlock()
	items := m.recent.last(limit)
	copied := make([]HookRequest, len(items))
	copy(copied, items)
	return copied
}

func (m *Manager) appendRequestLocked(hookID string, req HookRequest) error {
	path := m.requestsFile(hookID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	return enc.Encode(req)
}

func (m *Manager) requestsFile(hookID string) string {
	safe := strings.ReplaceAll(hookID, "..", "")
	return filepath.Join(m.requestsDir, safe+".jsonl")
}

// ExportRequests streams raw JSONL entries for a hook.
func (m *Manager) ExportRequests(hookID string, w io.Writer) error {
	m.mu.RLock()
	_, ok := m.hooks[hookID]
	m.mu.RUnlock()
	if !ok {
		return errors.New("hook not found")
	}
	path := m.requestsFile(hookID)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()
	_, err = io.Copy(w, f)
	return err
}

func newHookID() string {
	return "hook-" + randomHex(6)
}

func newRequestID() string {
	return "req-" + randomHex(6)
}

func newToken() string {
	return randomHex(12)
}

func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		// fallback to timestamp
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

// EncodeBodyPreview prepares a string preview for a request body.
func EncodeBodyPreview(body []byte) (preview string, encoding string, truncated bool) {
	if len(body) > maxPreviewBytes {
		truncated = true
		body = body[:maxPreviewBytes]
	}
	if utf8.Valid(body) {
		preview = string(body)
		encoding = "utf-8"
		return
	}
	encoding = "hex"
	preview = hex.EncodeToString(body)
	return
}

// ReadBody drains up to maxStoredBodyBytes from r.
func ReadBody(r io.Reader) ([]byte, int64, bool, error) {
	limited := io.LimitReader(r, maxStoredBodyBytes+1)
	buf, err := io.ReadAll(limited)
	if err != nil {
		return nil, 0, false, err
	}
	truncated := false
	size := int64(len(buf))
	if size > maxStoredBodyBytes {
		truncated = true
		buf = buf[:maxStoredBodyBytes]
		size = maxStoredBodyBytes
	}
	return buf, size, truncated, nil
}
