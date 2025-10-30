package middleware

import (
	"net/http"
	"strings"
	"sync"
	"time"
)

// RateLimiter implements token bucket rate limiting per IP
type RateLimiter struct {
	mu      sync.RWMutex
	buckets map[string]*bucket
	limit   int
	window  time.Duration
}

type bucket struct {
	tokens    int
	lastReset time.Time
}

// NewRateLimiter creates a new rate limiter with the specified limit and window
func NewRateLimiter(limit int, window time.Duration) *RateLimiter {
	rl := &RateLimiter{
		buckets: make(map[string]*bucket),
		limit:   limit,
		window:  window,
	}
	// Cleanup goroutine to prevent memory leak
	go func() {
		ticker := time.NewTicker(window)
		defer ticker.Stop()
		for range ticker.C {
			rl.cleanup()
		}
	}()
	return rl
}

func (rl *RateLimiter) allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	b, exists := rl.buckets[ip]

	if !exists || now.Sub(b.lastReset) > rl.window {
		rl.buckets[ip] = &bucket{
			tokens:    rl.limit - 1,
			lastReset: now,
		}
		return true
	}

	if b.tokens > 0 {
		b.tokens--
		return true
	}

	return false
}

func (rl *RateLimiter) cleanup() {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	for ip, b := range rl.buckets {
		if now.Sub(b.lastReset) > 2*rl.window {
			delete(rl.buckets, ip)
		}
	}
}

// WithRateLimit returns middleware that applies IP-based rate limiting
func WithRateLimit(rl *RateLimiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Extract IP address
			ip := r.RemoteAddr
			if colonPos := strings.LastIndex(ip, ":"); colonPos != -1 {
				ip = ip[:colonPos]
			}

			// Check for proxy headers
			if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
				if commaPos := strings.Index(xff, ","); commaPos != -1 {
					ip = strings.TrimSpace(xff[:commaPos])
				} else {
					ip = strings.TrimSpace(xff)
				}
			} else if xri := r.Header.Get("X-Real-IP"); xri != "" {
				ip = strings.TrimSpace(xri)
			}

			if !rl.allow(ip) {
				w.Header().Set("Retry-After", "60")
				http.Error(w, "Rate limit exceeded. Please try again later.", http.StatusTooManyRequests)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
