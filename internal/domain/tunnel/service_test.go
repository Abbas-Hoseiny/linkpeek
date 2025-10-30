package tunnel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewService(t *testing.T) {
    svc := NewService("/tmp", "cloudflared")
    if svc == nil {
        t.Fatal("NewService returned nil")
    }
    if svc.dataDir != "/tmp" {
        t.Errorf("expected dataDir /tmp, got %s", svc.dataDir)
    }
}

func TestExtractLastURLFromLog(t *testing.T) {
    svc := NewService("/tmp", "cloudflared")

    cases := []struct {
        name     string
        logData  string
        expected string
    }{
        {
            name:     "Full URL",
            logData:  "Some log text\nhttps://test-abc.trycloudflare.com\nmore text",
            expected: "https://test-abc.trycloudflare.com",
        },
        {
            name:     "JSON host field",
            logData:  `{"host": "test-def.trycloudflare.com", "other": "data"}`,
            expected: "https://test-def.trycloudflare.com",
        },
        {
            name:     "Bare domain",
            logData:  "Starting tunnel at test-ghi.trycloudflare.com with options",
            expected: "https://test-ghi.trycloudflare.com",
        },
        {
            name:     "No URL",
            logData:  "Some log text without any tunnel URL",
            expected: "",
        },
        {
            name:     "Multiple URLs - takes last",
            logData:  "https://first.trycloudflare.com\nhttps://second.trycloudflare.com",
            expected: "https://second.trycloudflare.com",
        },
    }

    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            got := svc.extractLastURLFromLog([]byte(tc.logData))
            if got != tc.expected {
                t.Errorf("expected %q, got %q", tc.expected, got)
            }
        })
    }
}

func TestNormalizeURL(t *testing.T) {
    svc := NewService("/tmp", "cloudflared")

    cases := []struct {
        input    string
        expected string
    }{
        {"https://test.trycloudflare.com", "https://test.trycloudflare.com"},
        {"https://test.trycloudflare.com/", "https://test.trycloudflare.com"},
        {"http://test.trycloudflare.com", "http://test.trycloudflare.com"},
        {"test.trycloudflare.com", "https://test.trycloudflare.com"},
        {"  https://test.trycloudflare.com  ", "https://test.trycloudflare.com"},
        {"", ""},
        {"https://not-a-tunnel.com", ""},
        {"https://test.trycloudflare.com/path", ""},
    }

    for _, tc := range cases {
        if got := svc.normalizeURL(tc.input); got != tc.expected {
            t.Errorf("normalizeURL(%q) = %q, expected %q", tc.input, got, tc.expected)
        }
    }
}

func TestRecordAndGetHistory(t *testing.T) {
    tmpDir := t.TempDir()
    svc := NewService(tmpDir, "cloudflared")

    svc.RecordURL("https://test1.trycloudflare.com")
    time.Sleep(5 * time.Millisecond)
    svc.RecordURL("https://test2.trycloudflare.com")
    svc.RecordURL("https://test1.trycloudflare.com")

    history := svc.GetHistory()
    if len(history) != 2 {
        t.Fatalf("expected 2 history items, got %d", len(history))
    }
    if history[0].SeenAt.Before(history[1].SeenAt) {
        t.Error("expected history to be sorted descending by SeenAt")
    }
    for _, item := range history {
        if item.SeenAt.IsZero() {
            t.Error("expected SeenAt to be populated")
        }
    }
}

func TestLoadHistory(t *testing.T) {
    tmpDir := t.TempDir()
    histPath := filepath.Join(tmpDir, "tunnel_history.jsonl")

    items := []HistoryItem{
        {URL: "https://test1.trycloudflare.com", SeenAt: time.Now().Add(-time.Minute)},
        {URL: "https://test2.trycloudflare.com", SeenAt: time.Now()},
    }

    f, err := os.Create(histPath)
    if err != nil {
        t.Fatalf("create history: %v", err)
    }
    for _, item := range items {
        data, _ := json.Marshal(item)
        if _, err := f.Write(append(data, '\n')); err != nil {
            t.Fatalf("write history: %v", err)
        }
    }
    f.Close()

    svc := NewService(tmpDir, "cloudflared")
    svc.LoadHistory()

    initial := len(svc.GetHistory())
    svc.RecordURL("https://test1.trycloudflare.com")
    if got := len(svc.GetHistory()); got != initial {
        t.Error("expected duplicate URL to be ignored")
    }
}

func TestClearHistory(t *testing.T) {
    tmpDir := t.TempDir()
    svc := NewService(tmpDir, "cloudflared")

    svc.RecordURL("https://test1.trycloudflare.com")
    svc.RecordURL("https://test2.trycloudflare.com")

    if err := svc.ClearHistory(); err != nil {
        t.Fatalf("ClearHistory failed: %v", err)
    }
    if len(svc.GetHistory()) != 0 {
        t.Error("expected history to be empty after ClearHistory")
    }
}

func TestRefreshStatus(t *testing.T) {
    tmpDir := t.TempDir()
    logPath := filepath.Join(tmpDir, "cloudflared.log")
    logData := "Starting tunnel\nhttps://test-xyz.trycloudflare.com\nTunnel started"
    if err := os.WriteFile(logPath, []byte(logData), 0o644); err != nil {
        t.Fatalf("failed to write log: %v", err)
    }

    statusPublished := false
    svc := NewService(tmpDir, "cloudflared")
    svc.SetPublishers(func(Status) {
        statusPublished = true
    }, nil)

    svc.RefreshStatus()

    status := svc.GetStatus()
    if !status.Active {
        t.Error("expected status to be active")
    }
    if status.URL != "https://test-xyz.trycloudflare.com" {
        t.Errorf("expected URL https://test-xyz.trycloudflare.com, got %s", status.URL)
    }
    if status.Since.IsZero() {
        t.Error("expected Since to be set")
    }

    time.Sleep(10 * time.Millisecond)
    if !statusPublished {
        t.Error("expected publisher to be invoked")
    }
}

func TestIsTunnelHost(t *testing.T) {
    svc := NewService("/tmp", "cloudflared")

    cases := []struct {
        host     string
        expected bool
    }{
        {"test.trycloudflare.com", true},
        {"abc-123.trycloudflare.com", true},
        {"example.com", false},
        {"sub.example.com", false},
    }

    for _, tc := range cases {
        if got := svc.IsTunnelHost(tc.host); got != tc.expected {
            t.Errorf("IsTunnelHost(%q) = %v, expected %v", tc.host, got, tc.expected)
        }
    }
}
