package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	minPasswordLength = 8
	defaultPassword   = "admin"
)

var (
	ErrPasswordTooShort   = errors.New("password must be at least 8 characters")
	ErrPasswordNoSpecial  = errors.New("password must contain at least one symbol or special character")
	ErrPasswordUnchanged  = errors.New("new password must differ from the current password")
	ErrPasswordNotSet     = errors.New("no password is configured")
)

// Store persists the administrative password hash on disk using bcrypt.
type Store struct {
	mu            sync.RWMutex
	path          string
	record        *record
	defaultActive bool
}

type record struct {
	PasswordHash string    `json:"password_hash"`
	UpdatedAt    time.Time `json:"updated_at"`
	Version      int       `json:"version"`
	MustChange   bool      `json:"must_change"`
}

// NewStore initialises the password store at the provided path.
// If the file exists it is loaded; otherwise the store starts without a password.
func NewStore(path string) (*Store, error) {
	abs := path
	if !filepath.IsAbs(path) {
		var err error
		abs, err = filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("resolve auth store path: %w", err)
		}
	}
	s := &Store{path: abs}
	if err := s.load(); err != nil {
		return nil, err
	}
	if s.record == nil {
		s.defaultActive = true
	}
	return s, nil
}

// HasPassword reports whether a password has been configured.
func (s *Store) HasPassword() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.defaultActive {
		return true
	}
	return s.record != nil && s.record.PasswordHash != ""
}

// Verify compares the supplied password with the stored hash.
func (s *Store) Verify(password string) bool {
	s.mu.RLock()
	rec := s.record
	defaultActive := s.defaultActive
	s.mu.RUnlock()
	if defaultActive {
		return password == defaultPassword
	}
	if rec == nil || rec.PasswordHash == "" {
		return false
	}
	if password == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(rec.PasswordHash), []byte(password)) == nil
}

// ChangePassword updates the password after verifying the current password.
func (s *Store) ChangePassword(current, next string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.defaultActive {
		if current != defaultPassword {
			return errors.New("current password is incorrect")
		}
		if err := validatePassword(next); err != nil {
			return err
		}
		if next == defaultPassword {
			return ErrPasswordUnchanged
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(next), bcrypt.DefaultCost)
		if err != nil {
			return fmt.Errorf("hash password: %w", err)
		}
		s.record = &record{
			PasswordHash: string(hash),
			UpdatedAt:    time.Now().UTC(),
			Version:      1,
			MustChange:   false,
		}
		s.defaultActive = false
		return s.persist()
	}
	if s.record == nil || s.record.PasswordHash == "" {
		return ErrPasswordNotSet
	}
	if err := bcrypt.CompareHashAndPassword([]byte(s.record.PasswordHash), []byte(current)); err != nil {
		return errors.New("current password is incorrect")
	}
	if err := validatePassword(next); err != nil {
		return err
	}
	if current == next {
		return ErrPasswordUnchanged
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(next), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	s.record.PasswordHash = string(hash)
	s.record.UpdatedAt = time.Now().UTC()
	s.record.MustChange = false
	return s.persist()
}

// MustChange reports whether users must change their password before full access.
func (s *Store) MustChange() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.defaultActive {
		return true
	}
	if s.record == nil {
		return true
	}
	return s.record.MustChange
}

// MarkMustChange forces a password change requirement.
func (s *Store) MarkMustChange() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.defaultActive {
		return nil
	}
	if s.record == nil {
		return nil
	}
	s.record.MustChange = true
	return s.persist()
}

// validatePassword enforces basic complexity requirements.
func validatePassword(password string) error {
	if len(password) < minPasswordLength {
		return ErrPasswordTooShort
	}
	symbol := false
	for _, r := range password {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		default:
			symbol = true
		}
	}
	if !symbol {
		return ErrPasswordNoSpecial
	}
	return nil
}

func (s *Store) load() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			s.defaultActive = true
			return nil
		}
		return fmt.Errorf("read auth store: %w", err)
	}
	var rec record
	if err := json.Unmarshal(data, &rec); err != nil {
		return fmt.Errorf("decode auth store: %w", err)
	}
	s.record = &rec
	s.defaultActive = false
	return nil
}

func (s *Store) persist() error {
	if s.record == nil {
		if err := os.Remove(s.path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove auth store: %w", err)
		}
		return nil
	}
	payload, err := json.MarshalIndent(s.record, "", "  ")
	if err != nil {
		return fmt.Errorf("encode auth store: %w", err)
	}
	tmpPath := s.path + ".tmp"
	if err := os.WriteFile(tmpPath, payload, 0o600); err != nil {
		return fmt.Errorf("write auth store: %w", err)
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		return fmt.Errorf("commit auth store: %w", err)
	}
	return nil
}

// PasswordRequirements returns a user-friendly summary of the policy.
func PasswordRequirements() string {
	return "Password must be at least 8 characters and contain at least one symbol."
}