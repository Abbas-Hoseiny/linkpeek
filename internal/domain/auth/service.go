package auth

import (
	"errors"
	"net/http"
	"time"

	"linkpeek/internal/auth"
	sessions "linkpeek/internal/domain/session"
)

// SessionManager captures the behaviour needed from the session manager implementation.
type SessionManager interface {
	Create() (string, time.Time)
	Validate(token string) (bool, time.Time)
	Delete(token string)
}

// Service bundles authentication helpers used by HTTP handlers.
type Service struct {
	store    *auth.Store
	sessions SessionManager
}

// NewService constructs a Service from the underlying store and session manager.
func NewService(store *auth.Store, sessions SessionManager) *Service {
	return &Service{store: store, sessions: sessions}
}

// VerifyPassword checks the provided password against the store.
func (s *Service) VerifyPassword(password string) bool {
	if s == nil || s.store == nil {
		return false
	}
	return s.store.Verify(password)
}

// MustChangePassword reports whether the operator must update the password.
func (s *Service) MustChangePassword() bool {
	if s == nil || s.store == nil {
		return true
	}
	return s.store.MustChange()
}

// ChangePassword attempts to update the stored password.
func (s *Service) ChangePassword(current, next string) error {
	if s == nil || s.store == nil {
		return errors.New("auth service unavailable")
	}
	return s.store.ChangePassword(current, next)
}

// CreateSession generates a new session token.
func (s *Service) CreateSession() (string, time.Time, error) {
	if s == nil || s.sessions == nil {
		return "", time.Time{}, errors.New("session manager unavailable")
	}
	token, expires := s.sessions.Create()
	if token == "" {
		return "", time.Time{}, errors.New("session unavailable")
	}
	return token, expires, nil
}

// ValidateSession checks whether the provided token is still valid.
func (s *Service) ValidateSession(token string) (bool, time.Time) {
	if s == nil || s.sessions == nil {
		return false, time.Time{}
	}
	return s.sessions.Validate(token)
}

// DeleteSession removes a session token.
func (s *Service) DeleteSession(token string) {
	if s == nil || s.sessions == nil {
		return
	}
	s.sessions.Delete(token)
}

// ReadSessionToken extracts the session token from the request.
func (s *Service) ReadSessionToken(r *http.Request) string {
	return sessions.ReadSessionToken(r)
}

// SetSessionCookie persists the session token in the response using secure flag awareness.
func (s *Service) SetSessionCookie(w http.ResponseWriter, token string, expires time.Time, secure bool) {
	sessions.SetSessionCookie(w, token, expires, secure)
}

// ClearSessionCookie removes any existing session cookie.
func (s *Service) ClearSessionCookie(w http.ResponseWriter) {
	sessions.ClearSessionCookie(w)
}

// PasswordRequirements returns the policy description for UI rendering.
func (s *Service) PasswordRequirements() string {
	return auth.PasswordRequirements()
}
