package snippet

import (
	"testing"
)

func TestNewManager(t *testing.T) {
	mgr := NewManager(64 * 1024)
	if mgr == nil {
		t.Fatal("NewManager returned nil")
	}
	if mgr.maxBytes != 64*1024 {
		t.Errorf("expected maxBytes to be %d, got %d", 64*1024, mgr.maxBytes)
	}
}

func TestCreateSnippet(t *testing.T) {
	mgr := NewManager(1024)

	content := []byte("console.log('hello world');")
	entry, err := mgr.Create(content, "text/javascript", "test.js")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	if entry.ID == "" {
		t.Error("expected non-empty ID")
	}
	if entry.Filename != "test.js" {
		t.Errorf("expected filename test.js, got %s", entry.Filename)
	}
	if entry.MIME != "text/javascript; charset=utf-8" {
		t.Errorf("expected MIME text/javascript; charset=utf-8, got %s", entry.MIME)
	}
	if entry.Size != len(content) {
		t.Errorf("expected size %d, got %d", len(content), entry.Size)
	}
	if entry.ETag == "" {
		t.Error("expected non-empty ETag")
	}
}

func TestCreateSnippetTooLarge(t *testing.T) {
	mgr := NewManager(10)

	content := []byte("This content is too large for the manager")
	_, err := mgr.Create(content, "text/plain", "test.txt")
	if err == nil {
		t.Error("expected error for content exceeding max size")
	}
}

func TestGetSnippet(t *testing.T) {
	mgr := NewManager(1024)

	content := []byte("test content")
	entry, err := mgr.Create(content, "text/plain", "test.txt")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	retrieved, ok := mgr.Get(entry.ID)
	if !ok {
		t.Error("expected snippet to be found")
	}
	if string(retrieved.Content) != string(content) {
		t.Errorf("expected content %s, got %s", content, retrieved.Content)
	}

	// Try to get non-existent snippet
	_, ok = mgr.Get("nonexistent")
	if ok {
		t.Error("expected snippet not to be found")
	}
}

func TestNormalizeMIME(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"", "text/plain; charset=utf-8"},
		{"  ", "text/plain; charset=utf-8"},
		{"TEXT/HTML", "TEXT/HTML; charset=utf-8"},
		{"application/json  ", "application/json"},
		{"text/x-shellscript", "text/x-shellscript; charset=utf-8"},
	}

	for _, tt := range tests {
		result := NormalizeMIME(tt.input)
		if result != tt.expected {
			t.Errorf("NormalizeMIME(%q) = %q, expected %q", tt.input, result, tt.expected)
		}
	}
}

func TestNormalizeFilename(t *testing.T) {
	tests := []struct {
		name     string
		mime     string
		expected string
	}{
		{"", "text/html", "snippet.html"},
		{"  ", "text/javascript", "snippet.js"},
		{"test.txt", "text/plain", "test.txt"},
		{"script", "text/javascript", "script"},
		{"style", "text/css", "style"},
		{"data", "application/json", "data"},
		{"/tmp/test.txt", "text/plain", "test.txt"},
	}

	for _, tt := range tests {
		result := NormalizeFilename(tt.name, tt.mime)
		if result != tt.expected {
			t.Errorf("NormalizeFilename(%q, %q) = %q, expected %q", tt.name, tt.mime, result, tt.expected)
		}
	}
}

func TestDefaultFilename(t *testing.T) {
	tests := []struct {
		mime     string
		expected string
	}{
		{"text/html", "snippet.html"},
		{"text/javascript", "snippet.js"},
		{"application/json", "snippet.json"},
		{"text/x-python", "snippet.txt"},
		{"text/x-shellscript", "snippet.sh"},
		{"unknown/type", "snippet.txt"},
	}

	for _, tt := range tests {
		result := DefaultFilename(tt.mime)
		if result != tt.expected {
			t.Errorf("DefaultFilename(%q) = %q, expected %q", tt.mime, result, tt.expected)
		}
	}
}

func TestExtractID(t *testing.T) {
	tests := []struct {
		path     string
		prefix   string
		expected string
		ok       bool
	}{
		{"/snippet/raw/abc123", "/snippet/raw/", "abc123", true},
		{"/snippet/html/def456", "/snippet/html/", "def456", true},
		{"/other/path/", "/snippet/raw/", "", false},
		{"/snippet/raw/", "/snippet/raw/", "", false},
		{"/snippet/raw", "/snippet/raw/", "", false},
	}

	for _, tt := range tests {
		id, ok := ExtractID(tt.path, tt.prefix)
		if ok != tt.ok {
			t.Errorf("ExtractID(%q, %q) ok = %v, expected %v", tt.path, tt.prefix, ok, tt.ok)
		}
		if id != tt.expected {
			t.Errorf("ExtractID(%q, %q) id = %q, expected %q", tt.path, tt.prefix, id, tt.expected)
		}
	}
}
