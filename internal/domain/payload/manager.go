package payload

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"linkpeek/internal/types"
)

// Manager manages payload storage and metadata
type Manager struct {
	mu             sync.RWMutex
	payloads       map[string]*types.PayloadMeta
	dataDir        string
	payloadDir     string
	maxUploadBytes int64
	publisher      func() // Callback to publish payload list updates
}

// NewManager creates a new payload manager
func NewManager(dataDir string, maxUploadBytes int64, publisher func()) (*Manager, error) {
	payloadDir := filepath.Join(dataDir, "payloads")
	if err := os.MkdirAll(payloadDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create payload directory: %w", err)
	}

	mgr := &Manager{
		payloads:       make(map[string]*types.PayloadMeta),
		dataDir:        dataDir,
		payloadDir:     payloadDir,
		maxUploadBytes: maxUploadBytes,
		publisher:      publisher,
	}

	// Load existing payloads
	if err := mgr.loadIndex(); err != nil {
		return nil, fmt.Errorf("failed to load payload index: %w", err)
	}

	return mgr, nil
}

// Create stores a new payload from uploaded data
func (m *Manager) Create(data []byte, originalFilename, name, category, mimeType string) (*types.PayloadMeta, error) {
	if int64(len(data)) > m.maxUploadBytes {
		return nil, fmt.Errorf("payload exceeds maximum size of %d bytes", m.maxUploadBytes)
	}

	if mimeType == "" {
		mimeType = mime.TypeByExtension(filepath.Ext(originalFilename))
		if mimeType == "" {
			mimeType = "application/octet-stream"
		}
	}

	meta := &types.PayloadMeta{
		ID:               NewPayloadID(),
		Name:             name,
		Category:         category,
		OriginalFilename: originalFilename,
		Size:             int64(len(data)),
		MimeType:         mimeType,
		CreatedAt:        time.Now().UTC(),
		UpdatedAt:        time.Now().UTC(),
	}

	// Create payload directory
	payloadPath := filepath.Join(m.payloadDir, meta.ID)
	if err := os.MkdirAll(payloadPath, 0755); err != nil {
		return nil, fmt.Errorf("failed to create payload directory: %w", err)
	}

	// Write payload file
	dataPath := filepath.Join(payloadPath, "original.bin")
	if err := os.WriteFile(dataPath, data, 0644); err != nil {
		return nil, fmt.Errorf("failed to write payload file: %w", err)
	}

	// Save metadata
	if err := m.saveMeta(meta); err != nil {
		return nil, fmt.Errorf("failed to save payload metadata: %w", err)
	}

	var publisher func()
	m.mu.Lock()
	m.payloads[meta.ID] = meta
	publisher = m.publisher
	m.mu.Unlock()

	if publisher != nil {
		go publisher()
	}

	return meta, nil
}

// Get retrieves payload metadata by ID
func (m *Manager) Get(id string) (*types.PayloadMeta, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	meta, ok := m.payloads[id]
	return meta, ok
}

// List returns all payload metadata
func (m *Manager) List() []types.PayloadMeta {
	m.mu.RLock()
	defer m.mu.RUnlock()
	list := make([]types.PayloadMeta, 0, len(m.payloads))
	for _, meta := range m.payloads {
		list = append(list, *meta)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].CreatedAt.Equal(list[j].CreatedAt) {
			return list[i].ID > list[j].ID
		}
		return list[i].CreatedAt.After(list[j].CreatedAt)
	})
	return list
}

// Delete removes a payload and its data
func (m *Manager) Delete(id string) error {
	var publisher func()
	m.mu.Lock()
	meta, ok := m.payloads[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("payload not found")
	}
	delete(m.payloads, id)
	publisher = m.publisher
	m.mu.Unlock()

	// Remove payload directory
	payloadPath := filepath.Join(m.payloadDir, meta.ID)
	if err := os.RemoveAll(payloadPath); err != nil {
		return fmt.Errorf("failed to remove payload directory: %w", err)
	}

	if publisher != nil {
		go publisher()
	}

	return nil
}

// GetFilePath returns the path to the payload file
func (m *Manager) GetFilePath(id string) (string, error) {
	meta, ok := m.Get(id)
	if !ok {
		return "", fmt.Errorf("payload not found")
	}
	return filepath.Join(m.payloadDir, meta.ID, "original.bin"), nil
}

// OpenFile opens the payload file for reading
func (m *Manager) OpenFile(id string) (*os.File, os.FileInfo, error) {
	path, err := m.GetFilePath(id)
	if err != nil {
		return nil, nil, err
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to open payload file: %w", err)
	}

	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, fmt.Errorf("failed to stat payload file: %w", err)
	}

	return f, info, nil
}

// saveMeta writes payload metadata to disk
func (m *Manager) saveMeta(meta *types.PayloadMeta) error {
	metaPath := filepath.Join(m.payloadDir, meta.ID, "meta.json")
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(metaPath, data, 0644)
}

// loadMeta reads payload metadata from disk
func (m *Manager) loadMeta(id string) (*types.PayloadMeta, error) {
	metaPath := filepath.Join(m.payloadDir, id, "meta.json")
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return nil, err
	}

	var meta types.PayloadMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, err
	}
	return &meta, nil
}

// loadIndex loads all payload metadata from disk
func (m *Manager) loadIndex() error {
	entries, err := os.ReadDir(m.payloadDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		meta, err := m.loadMeta(entry.Name())
		if err != nil {
			// Skip invalid entries
			continue
		}

		m.payloads[meta.ID] = meta
	}

	return nil
}

// GetVariants returns all available variant paths for a payload
func (m *Manager) GetVariants(id string) []types.PayloadVariant {
	return []types.PayloadVariant{
		{Key: "raw", Path: fmt.Sprintf("/payload/raw/%s", id)},
		{Key: "inline", Path: fmt.Sprintf("/payload/inline/%s", id)},
		{Key: "download", Path: fmt.Sprintf("/payload/download/%s", id)},
		{Key: "mime-mismatch", Path: fmt.Sprintf("/payload/mime-mismatch/%s", id)},
		{Key: "corrupt", Path: fmt.Sprintf("/payload/corrupt/%s", id)},
		{Key: "slow", Path: fmt.Sprintf("/payload/slow/%s", id)},
		{Key: "chunked", Path: fmt.Sprintf("/payload/chunked/%s", id)},
		{Key: "redirect", Path: fmt.Sprintf("/payload/redirect/%s", id)},
		{Key: "error", Path: fmt.Sprintf("/payload/error/%s", id)},
		{Key: "range", Path: fmt.Sprintf("/payload/range/%s", id)},
		{Key: "spectrum", Path: fmt.Sprintf("/payload/spec/%s/0", id)},
	}
}

// Snapshot returns payload list items for realtime updates
func (m *Manager) Snapshot() []types.PayloadListItem {
	m.mu.RLock()
	defer m.mu.RUnlock()

	items := make([]types.PayloadListItem, 0, len(m.payloads))
	for _, meta := range m.payloads {
		items = append(items, types.PayloadListItem{
			Payload:  *meta,
			Variants: m.GetVariants(meta.ID),
		})
	}
	sort.Slice(items, func(i, j int) bool {
		iMeta := items[i].Payload
		jMeta := items[j].Payload
		if iMeta.CreatedAt.Equal(jMeta.CreatedAt) {
			return iMeta.ID > jMeta.ID
		}
		return iMeta.CreatedAt.After(jMeta.CreatedAt)
	})
	return items
}

// MaxUploadBytes returns the maximum allowed upload size.
func (m *Manager) MaxUploadBytes() int64 {
	if m == nil {
		return 0
	}
	return m.maxUploadBytes
}

// SetPublisher configures the callback invoked after mutations.
func (m *Manager) SetPublisher(fn func()) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.publisher = fn
	m.mu.Unlock()
}

// NewPayloadID generates a unique payload ID
func NewPayloadID() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err == nil {
		return "p-" + hex.EncodeToString(buf)
	}
	return fmt.Sprintf("p-%d", time.Now().UnixNano())
}

// ExtractIDFromPath extracts payload ID from a URL path with the given prefix
func ExtractIDFromPath(path, prefix string) (string, bool) {
	if len(path) <= len(prefix) {
		return "", false
	}
	if path[:len(prefix)] != prefix {
		return "", false
	}
	id := path[len(prefix):]
	if id == "" {
		return "", false
	}
	return id, true
}
