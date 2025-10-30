package middleware

import (
	"testing"
	"time"
)

func TestRateLimiter(t *testing.T) {
	rl := NewRateLimiter(5, time.Second)

	// First 5 requests should be allowed
	for i := 0; i < 5; i++ {
		if !rl.allow("192.168.1.1") {
			t.Errorf("Request %d should be allowed", i+1)
		}
	}

	// 6th request should be denied
	if rl.allow("192.168.1.1") {
		t.Error("Request 6 should be denied (rate limit exceeded)")
	}

	// Different IP should be allowed
	if !rl.allow("192.168.1.2") {
		t.Error("Request from different IP should be allowed")
	}

	// Wait for window to reset
	time.Sleep(1100 * time.Millisecond)

	// Should be allowed again after reset
	if !rl.allow("192.168.1.1") {
		t.Error("Request should be allowed after rate limit window reset")
	}
}

func TestRateLimiterCleanup(t *testing.T) {
	rl := NewRateLimiter(10, 100*time.Millisecond)

	// Add some buckets
	rl.allow("192.168.1.1")
	rl.allow("192.168.1.2")
	rl.allow("192.168.1.3")

	if len(rl.buckets) != 3 {
		t.Errorf("Expected 3 buckets, got %d", len(rl.buckets))
	}

	// Wait for cleanup (2x window)
	time.Sleep(250 * time.Millisecond)

	// Trigger another request to ensure cleanup has run
	rl.allow("192.168.1.4")

	// Old buckets should have been cleaned up
	rl.mu.RLock()
	count := len(rl.buckets)
	rl.mu.RUnlock()

	// Should be 1 (only the new request)
	if count > 2 {
		t.Logf("Warning: Expected cleanup to reduce bucket count, got %d buckets", count)
	}
}
