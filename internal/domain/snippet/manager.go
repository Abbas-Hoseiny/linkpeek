package snippet

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Entry represents a code snippet stored in memory
type Entry struct {
	ID        string
	Filename  string
	MIME      string
	Content   []byte
	Size      int
	CreatedAt time.Time
	ETag      string
}

// Manager handles in-memory storage of code snippets
type Manager struct {
	mu       sync.RWMutex
	snippets map[string]*Entry
	maxBytes int
}

// NewManager creates a new snippet manager with the specified max size
func NewManager(maxBytes int) *Manager {
	return &Manager{
		snippets: make(map[string]*Entry),
		maxBytes: maxBytes,
	}
}

// Create generates a new snippet and stores it
func (m *Manager) Create(content []byte, mime, filename string) (*Entry, error) {
	if len(content) > m.maxBytes {
		return nil, fmt.Errorf("snippet too large (max %d bytes)", m.maxBytes)
	}

	mime = NormalizeMIME(mime)
	filename = NormalizeFilename(filename, mime)

	// Generate ETag from content
	id := newSnippetID()
	hash := sha256.Sum256(content)
	etag := fmt.Sprintf("W/\"snippet-%s-%x\"", id, hash[:6])

	entry := &Entry{
		ID:        id,
		Filename:  filename,
		MIME:      mime,
		Content:   content,
		Size:      len(content),
		CreatedAt: time.Now().UTC(),
		ETag:      etag,
	}

	m.mu.Lock()
	m.snippets[entry.ID] = entry
	m.mu.Unlock()

	return entry, nil
}

// Get retrieves a snippet by ID
func (m *Manager) Get(id string) (*Entry, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	entry, ok := m.snippets[id]
	if !ok {
		return nil, false
	}
	copyEntry := *entry
	copyEntry.Content = append([]byte(nil), entry.Content...)
	return &copyEntry, true
}

// MaxBytes returns the configured snippet size limit.
func (m *Manager) MaxBytes() int {
	if m == nil {
		return 0
	}
	return m.maxBytes
}

// newSnippetID generates a unique snippet ID
func newSnippetID() string {
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err == nil {
		return "s-" + hex.EncodeToString(buf)
	}
	return fmt.Sprintf("s-%d", time.Now().UnixNano())
}

// NormalizeMIME ensures consistent MIME type formatting
func NormalizeMIME(m string) string {
	m = strings.TrimSpace(m)
	if m == "" {
		return "text/plain; charset=utf-8"
	}
	lower := strings.ToLower(m)
	if strings.HasPrefix(lower, "text/") && !strings.Contains(lower, "charset") {
		return m + "; charset=utf-8"
	}
	return m
}

// NormalizeFilename ensures the filename has an appropriate extension
func NormalizeFilename(name, mime string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return DefaultFilename(mime)
	}
	name = filepath.Base(name)
	if name == "." || name == ".." || name == "" {
		return DefaultFilename(mime)
	}
	return name
}

// DefaultFilename generates a default filename based on MIME type
func DefaultFilename(mime string) string {
	base := "snippet"
	ml := strings.ToLower(mime)
	switch {
	case strings.Contains(ml, "javascript"):
		return base + ".js"
	case strings.Contains(ml, "shell") || strings.Contains(ml, "bash"):
		return base + ".sh"
	case strings.Contains(ml, "html"):
		return base + ".html"
	case strings.Contains(ml, "json"):
		return base + ".json"
	case strings.Contains(ml, "xml"):
		return base + ".xml"
	default:
		return base + ".txt"
	}
}

// ExtractID extracts snippet ID from a URL path with the given prefix
func ExtractID(path, prefix string) (string, bool) {
	if !strings.HasPrefix(path, prefix) {
		return "", false
	}
	id := strings.TrimPrefix(path, prefix)
	id = strings.Trim(id, "/")
	if id == "" {
		return "", false
	}
	return id, true
}
