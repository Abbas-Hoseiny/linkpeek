package auth

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestStoreDefaultPasswordFlow(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "auth.json")
	s, err := NewStore(storePath)
	if err != nil {
		t.Fatalf("NewStore error: %v", err)
	}
	if !s.HasPassword() {
		t.Fatal("expected default password to be active")
	}
	if !s.MustChange() {
		t.Fatal("default password should require change")
	}
	if !s.Verify("admin") {
		t.Fatal("default password should verify")
	}
	if s.Verify("wrong") {
		t.Fatal("unexpected verification success for wrong password")
	}
	if err := s.ChangePassword("wrong", "Sup3r$ecret!"); err == nil {
		t.Fatal("expected error for incorrect current password")
	}
	if err := s.ChangePassword("admin", "Sup3r$ecret!"); err != nil {
		t.Fatalf("ChangePassword error: %v", err)
	}
	if s.MustChange() {
		t.Fatal("password change should clear must-change flag")
	}
	if !s.Verify("Sup3r$ecret!") {
		t.Fatal("new password should verify")
	}
	if s.Verify("admin") {
		t.Fatal("old default password should no longer verify")
	}
	loaded, err := NewStore(storePath)
	if err != nil {
		t.Fatalf("reload NewStore error: %v", err)
	}
	if loaded.MustChange() {
		t.Fatal("reloaded store should remember cleared must-change flag")
	}
	if !loaded.Verify("Sup3r$ecret!") {
		t.Fatal("reloaded store should verify new password")
	}
}

func TestChangePasswordValidations(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "auth.json")
	s, err := NewStore(storePath)
	if err != nil {
		t.Fatalf("NewStore error: %v", err)
	}
	// Too short when changing from default
	if err := s.ChangePassword("admin", "short!"); !errors.Is(err, ErrPasswordTooShort) {
		t.Fatalf("expected ErrPasswordTooShort, got %v", err)
	}
	// Missing symbol
	if err := s.ChangePassword("admin", "Password1"); !errors.Is(err, ErrPasswordNoSpecial) {
		t.Fatalf("expected ErrPasswordNoSpecial, got %v", err)
	}
	if err := s.ChangePassword("admin", "Sup3r$ecret!"); err != nil {
		t.Fatalf("initial change error: %v", err)
	}
	// New password same as current
	if err := s.ChangePassword("Sup3r$ecret!", "Sup3r$ecret!"); !errors.Is(err, ErrPasswordUnchanged) {
		t.Fatalf("expected ErrPasswordUnchanged, got %v", err)
	}
}
