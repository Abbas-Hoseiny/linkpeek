package utils

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNowUTC(t *testing.T) {
	now := NowUTC()
	if now.Location() != time.UTC {
		t.Errorf("NowUTC() should return time in UTC location, got %v", now.Location())
	}
	if time.Since(now) > time.Second {
		t.Errorf("NowUTC() should return current time, got time from %v ago", time.Since(now))
	}
}

func TestReqID(t *testing.T) {
	start := time.Now()
	reqID := ReqID(start)

	if !strings.HasPrefix(reqID, "r-") {
		t.Errorf("ReqID should start with 'r-', got %s", reqID)
	}

	// Test that different times produce different IDs
	time.Sleep(time.Millisecond)
	reqID2 := ReqID(time.Now())
	if reqID == reqID2 {
		t.Errorf("Different timestamps should produce different request IDs")
	}
}

func TestRemoteIP(t *testing.T) {
	tests := []struct {
		name       string
		remoteAddr string
		headers    map[string]string
		expectedIP string
	}{
		{
			name:       "CF-Connecting-IP header",
			remoteAddr: "10.0.0.1:12345",
			headers:    map[string]string{"CF-Connecting-IP": "1.2.3.4"},
			expectedIP: "1.2.3.4",
		},
		{
			name:       "X-Forwarded-For header",
			remoteAddr: "10.0.0.1:12345",
			headers:    map[string]string{"X-Forwarded-For": "1.2.3.4, 5.6.7.8"},
			expectedIP: "1.2.3.4",
		},
		{
			name:       "X-Real-IP header",
			remoteAddr: "10.0.0.1:12345",
			headers:    map[string]string{"X-Real-IP": "1.2.3.4"},
			expectedIP: "1.2.3.4",
		},
		{
			name:       "RemoteAddr fallback",
			remoteAddr: "1.2.3.4:12345",
			headers:    map[string]string{},
			expectedIP: "1.2.3.4",
		},
		{
			name:       "IPv6 address with brackets",
			remoteAddr: "[2001:db8::1]:12345",
			headers:    map[string]string{},
			expectedIP: "2001:db8::1",
		},
		{
			name:       "CF-Connecting-IP takes precedence",
			remoteAddr: "10.0.0.1:12345",
			headers: map[string]string{
				"CF-Connecting-IP": "1.2.3.4",
				"X-Forwarded-For":  "5.6.7.8",
			},
			expectedIP: "1.2.3.4",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/", nil)
			req.RemoteAddr = tt.remoteAddr
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}

			ip := RemoteIP(req)
			if ip != tt.expectedIP {
				t.Errorf("RemoteIP() = %v, want %v", ip, tt.expectedIP)
			}
		})
	}
}

func TestHeaderMap(t *testing.T) {
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "test-agent")
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Host = "example.com"

	headers := HeaderMap(req)

	// Check that the map is returned and has some expected keys
	if headers == nil {
		t.Fatal("HeaderMap() should return a non-nil map")
	}

	// Check that Host is correctly captured
	if headers["host"] != "example.com" {
		t.Errorf("Expected host to be 'example.com', got '%s'", headers["host"])
	}

	// Check that headers are correctly captured
	if headers["accept"] != "application/json" {
		t.Errorf("Expected accept to be 'application/json', got '%s'", headers["accept"])
	}

	if headers["x-forwarded-for"] != "1.2.3.4" {
		t.Errorf("Expected x-forwarded-for to be '1.2.3.4', got '%s'", headers["x-forwarded-for"])
	}

	if headers["sec-fetch-site"] != "same-origin" {
		t.Errorf("Expected sec-fetch-site to be 'same-origin', got '%s'", headers["sec-fetch-site"])
	}

	// Check that missing headers return empty string
	if headers["dnt"] != "" {
		t.Errorf("Expected missing header 'dnt' to be empty string, got '%s'", headers["dnt"])
	}
}
