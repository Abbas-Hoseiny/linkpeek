package capture

import (
	"os"
	"path/filepath"
	"testing"
)

func TestManagerLifecycle(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(dir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	hooks := m.ListHooks()
	if len(hooks) != 0 {
		t.Fatalf("expected empty hook list, got %d", len(hooks))
	}

	hook, err := m.CreateHook("Test Hook")
	if err != nil {
		t.Fatalf("CreateHook: %v", err)
	}
	if hook.Label != "Test Hook" {
		t.Fatalf("expected label 'Test Hook', got %q", hook.Label)
	}

	body := []byte("hello world")
	preview, enc, _ := EncodeBodyPreview(body)
	if enc != "utf-8" || preview != "hello world" {
		t.Fatalf("unexpected preview: %s/%s", preview, enc)
	}

	req := HookRequest{
		Method:       "POST",
		Path:         "/",
		BodyPreview:  preview,
		BodyEncoding: enc,
	}
	recorded, err := m.RecordRequest(hook.ID, req)
	if err != nil {
		t.Fatalf("RecordRequest: %v", err)
	}
	if recorded.ID == "" {
		t.Fatalf("expected request ID")
	}

	list := m.ListRequests(hook.ID, 10)
	if len(list) != 1 {
		t.Fatalf("expected 1 request, got %d", len(list))
	}

	recent := m.RecentRequests(5)
	if len(recent) != 1 {
		t.Fatalf("expected recent requests, got %d", len(recent))
	}

	if err := m.ClearRequests(hook.ID); err != nil {
		t.Fatalf("ClearRequests: %v", err)
	}
	if got := len(m.ListRequests(hook.ID, 10)); got != 0 {
		t.Fatalf("expected 0 after clear, got %d", got)
	}

	if err := m.DeleteHook(hook.ID); err != nil {
		t.Fatalf("DeleteHook: %v", err)
	}
	hooks = m.ListHooks()
	if len(hooks) != 0 {
		t.Fatalf("expected 0 hooks, got %d", len(hooks))
	}

	// Ensure files removed
	if _, err := os.Stat(filepath.Join(dir, hooksFilename)); err != nil {
		if !os.IsNotExist(err) {
			t.Fatalf("expected hooks file removed, got err %v", err)
		}
	}
}
