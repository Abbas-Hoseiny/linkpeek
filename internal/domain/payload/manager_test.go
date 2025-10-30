package payload

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewManager(t *testing.T) {
	tmpDir := t.TempDir()
	mgr, err := NewManager(tmpDir, 1024*1024, nil)
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	if mgr == nil {
		t.Fatal("NewManager returned nil")
	}

	// Verify payload directory was created
	payloadDir := filepath.Join(tmpDir, "payloads")
	if _, err := os.Stat(payloadDir); os.IsNotExist(err) {
		t.Error("payload directory was not created")
	}
}

func TestCreatePayload(t *testing.T) {
	tmpDir := t.TempDir()
	mgr, _ := NewManager(tmpDir, 1024*1024, nil)

	data := []byte("test payload content")
	meta, err := mgr.Create(data, "test.txt", "Test Payload", "test", "text/plain")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	if meta.ID == "" {
		t.Error("expected non-empty payload ID")
	}
	if meta.Name != "Test Payload" {
		t.Errorf("expected name 'Test Payload', got %s", meta.Name)
	}
	if meta.Category != "test" {
		t.Errorf("expected category 'test', got %s", meta.Category)
	}
	if meta.Size != int64(len(data)) {
		t.Errorf("expected size %d, got %d", len(data), meta.Size)
	}
	if meta.MimeType != "text/plain" {
		t.Errorf("expected MIME type text/plain, got %s", meta.MimeType)
	}

	// Verify file was written
	path, err := mgr.GetFilePath(meta.ID)
	if err != nil {
		t.Errorf("GetFilePath failed: %v", err)
	}
	savedData, err := os.ReadFile(path)
	if err != nil {
		t.Errorf("failed to read saved file: %v", err)
	}
	if string(savedData) != string(data) {
		t.Error("saved data does not match original")
	}
}

func TestCreatePayloadTooLarge(t *testing.T) {
	tmpDir := t.TempDir()
	mgr, _ := NewManager(tmpDir, 100, nil)

	data := make([]byte, 200)
	_, err := mgr.Create(data, "large.bin", "Large File", "", "")
	if err == nil {
		t.Error("expected error for payload exceeding max size")
	}
}

func TestGetPayload(t *testing.T) {
	tmpDir := t.TempDir()
	mgr, _ := NewManager(tmpDir, 1024*1024, nil)

	data := []byte("test content")
	created, _ := mgr.Create(data, "test.txt", "Test", "", "")

	retrieved, ok := mgr.Get(created.ID)
	if !ok {
		t.Error("expected payload to be found")
	}
	if retrieved.ID != created.ID {
		t.Errorf("expected ID %s, got %s", created.ID, retrieved.ID)
	}

	// Try to get non-existent payload
	_, ok = mgr.Get("nonexistent")
	if ok {
		t.Error("expected payload not to be found")
	}
}

func TestListPayloads(t *testing.T) {
	tmpDir := t.TempDir()
	mgr, _ := NewManager(tmpDir, 1024*1024, nil)

	mgr.Create([]byte("data1"), "file1.txt", "Payload 1", "", "")
	mgr.Create([]byte("data2"), "file2.txt", "Payload 2", "", "")

	list := mgr.List()
	if len(list) != 2 {
		t.Errorf("expected 2 payloads, got %d", len(list))
	}
}

func TestDeletePayload(t *testing.T) {
	tmpDir := t.TempDir()
	mgr, _ := NewManager(tmpDir, 1024*1024, nil)

	data := []byte("test content")
	meta, _ := mgr.Create(data, "test.txt", "Test", "", "")

	err := mgr.Delete(meta.ID)
	if err != nil {
		t.Errorf("Delete failed: %v", err)
	}

	// Verify it's gone
	_, ok := mgr.Get(meta.ID)
	if ok {
		t.Error("expected payload to be deleted")
	}

	// Verify directory was removed
	payloadPath := filepath.Join(tmpDir, "payloads", meta.ID)
	if _, err := os.Stat(payloadPath); !os.IsNotExist(err) {
		t.Error("expected payload directory to be removed")
	}

	// Try to delete non-existent payload
	err = mgr.Delete("nonexistent")
	if err == nil {
		t.Error("expected error when deleting non-existent payload")
	}
}

func TestOpenFile(t *testing.T) {
	tmpDir := t.TempDir()
	mgr, _ := NewManager(tmpDir, 1024*1024, nil)

	data := []byte("test file content")
	meta, _ := mgr.Create(data, "test.txt", "Test", "", "")

	f, info, err := mgr.OpenFile(meta.ID)
	if err != nil {
		t.Fatalf("OpenFile failed: %v", err)
	}
	defer f.Close()

	if info.Size() != int64(len(data)) {
		t.Errorf("expected file size %d, got %d", len(data), info.Size())
	}

	// Read and verify content
	readData := make([]byte, len(data))
	n, err := f.Read(readData)
	if err != nil {
		t.Errorf("failed to read file: %v", err)
	}
	if n != len(data) {
		t.Errorf("expected to read %d bytes, got %d", len(data), n)
	}
	if string(readData) != string(data) {
		t.Error("read data does not match original")
	}
}

func TestGetVariants(t *testing.T) {
	tmpDir := t.TempDir()
	mgr, _ := NewManager(tmpDir, 1024*1024, nil)

	variants := mgr.GetVariants("test-id")
	if len(variants) != 11 {
		t.Errorf("expected 11 variants, got %d", len(variants))
	}

	expectedKeys := []string{"raw", "inline", "download", "mime-mismatch", "corrupt", "slow", "chunked", "redirect", "error", "range", "spectrum"}
	for i, key := range expectedKeys {
		if variants[i].Key != key {
			t.Errorf("expected variant key %s at position %d, got %s", key, i, variants[i].Key)
		}
	}
}

func TestSnapshot(t *testing.T) {
	tmpDir := t.TempDir()
	mgr, _ := NewManager(tmpDir, 1024*1024, nil)

	mgr.Create([]byte("data1"), "file1.txt", "Payload 1", "", "")
	mgr.Create([]byte("data2"), "file2.txt", "Payload 2", "", "")

	snapshot := mgr.Snapshot()
	if len(snapshot) != 2 {
		t.Errorf("expected 2 items in snapshot, got %d", len(snapshot))
	}

	for _, item := range snapshot {
		if len(item.Variants) != 11 {
			t.Errorf("expected 11 variants per item, got %d", len(item.Variants))
		}
	}
}

func TestLoadIndex(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a payload with first manager
	mgr1, _ := NewManager(tmpDir, 1024*1024, nil)
	data := []byte("persistent data")
	meta1, _ := mgr1.Create(data, "test.txt", "Test Payload", "test", "text/plain")

	// Create a second manager - should load existing payloads
	mgr2, err := NewManager(tmpDir, 1024*1024, nil)
	if err != nil {
		t.Fatalf("second NewManager failed: %v", err)
	}

	retrieved, ok := mgr2.Get(meta1.ID)
	if !ok {
		t.Error("expected payload to be loaded from disk")
	}
	if retrieved.Name != meta1.Name {
		t.Errorf("expected name %s, got %s", meta1.Name, retrieved.Name)
	}
}

func TestExtractIDFromPath(t *testing.T) {
	tests := []struct {
		path     string
		prefix   string
		expected string
		ok       bool
	}{
		{"/payload/raw/abc123", "/payload/raw/", "abc123", true},
		{"/payload/inline/def456", "/payload/inline/", "def456", true},
		{"/other/path/", "/payload/raw/", "", false},
		{"/payload/raw/", "/payload/raw/", "", false},
		{"/payload/raw", "/payload/raw/", "", false},
	}

	for _, tt := range tests {
		id, ok := ExtractIDFromPath(tt.path, tt.prefix)
		if ok != tt.ok {
			t.Errorf("ExtractIDFromPath(%q, %q) ok = %v, expected %v", tt.path, tt.prefix, ok, tt.ok)
		}
		if id != tt.expected {
			t.Errorf("ExtractIDFromPath(%q, %q) id = %q, expected %q", tt.path, tt.prefix, id, tt.expected)
		}
	}
}
