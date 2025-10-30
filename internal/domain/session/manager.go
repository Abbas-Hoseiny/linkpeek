package session

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"sync"
	"time"
)

const sessionCookieName = "lp_session"

// Manager handles user session lifecycle
type Manager struct {
	mu     sync.RWMutex
	ttl    time.Duration
	items  map[string]time.Time
	stopCh chan struct{}
}

// NewManager creates a new session manager with the given TTL
func NewManager(ttl time.Duration) *Manager {
	sm := &Manager{
		ttl:    ttl,
		items:  make(map[string]time.Time),
		stopCh: make(chan struct{}),
	}
	go sm.cleanup()
	return sm
}

// Create generates a new session token and returns it with its expiration time
func (sm *Manager) Create() (string, time.Time) {
	if sm == nil {
		return "", time.Time{}
	}
	token := newSessionToken()
	expires := time.Now().Add(sm.ttl)
	sm.mu.Lock()
	sm.items[token] = expires
	sm.mu.Unlock()
	return token, expires
}

// Validate checks if a session token is valid and extends its expiration
func (sm *Manager) Validate(token string) (bool, time.Time) {
	if sm == nil || token == "" {
		return false, time.Time{}
	}
	sm.mu.Lock()
	expires, ok := sm.items[token]
	if !ok {
		sm.mu.Unlock()
		return false, time.Time{}
	}
	if time.Now().After(expires) {
		delete(sm.items, token)
		sm.mu.Unlock()
		return false, time.Time{}
	}
	expires = time.Now().Add(sm.ttl)
	sm.items[token] = expires
	sm.mu.Unlock()
	return true, expires
}

// Delete removes a session token
func (sm *Manager) Delete(token string) {
	if sm == nil || token == "" {
		return
	}
	sm.mu.Lock()
	delete(sm.items, token)
	sm.mu.Unlock()
}

// cleanup periodically removes expired sessions
func (sm *Manager) cleanup() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			now := time.Now()
			sm.mu.Lock()
			for token, exp := range sm.items {
				if now.After(exp) {
					delete(sm.items, token)
				}
			}
			sm.mu.Unlock()
		case <-sm.stopCh:
			return
		}
	}
}

// Close stops the cleanup goroutine
func (sm *Manager) Close() {
	if sm == nil {
		return
	}
	sm.mu.Lock()
	if sm.stopCh != nil {
		close(sm.stopCh)
		sm.stopCh = nil
	}
	sm.mu.Unlock()
}

// newSessionToken generates a random session token
func newSessionToken() string {
	buf := make([]byte, 18)
	if _, err := rand.Read(buf); err == nil {
		return hex.EncodeToString(buf)
	}
	return fmt.Sprintf("s-%d", time.Now().UnixNano())
}

// ReadSessionToken extracts the session token from request cookies or headers
func ReadSessionToken(r *http.Request) string {
	if c, err := r.Cookie(sessionCookieName); err == nil && c.Value != "" {
		return c.Value
	}
	return ""
}

// SetSessionCookie sets the session cookie on the response.
func SetSessionCookie(w http.ResponseWriter, token string, expires time.Time, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// ClearSessionCookie removes the session cookie.
func ClearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		SameSite: http.SameSiteLaxMode,
	})
}
