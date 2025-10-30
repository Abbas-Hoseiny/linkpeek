package auth_test

import (
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"linkpeek/internal/auth"
	domainauth "linkpeek/internal/domain/auth"
)

type stubSessionManager struct {
	createToken    string
	createExpires  time.Time
	validateOK     bool
	validateExpiry time.Time
	lastValidated  string
	deleted        []string
}

func (s *stubSessionManager) Create() (string, time.Time) {
	return s.createToken, s.createExpires
}

func (s *stubSessionManager) Validate(token string) (bool, time.Time) {
	s.lastValidated = token
	return s.validateOK, s.validateExpiry
}

func (s *stubSessionManager) Delete(token string) {
	s.deleted = append(s.deleted, token)
}

func TestServiceVerifyAndChangePassword(t *testing.T) {
	store, err := auth.NewStore(filepath.Join(t.TempDir(), "auth.json"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	mgr := &stubSessionManager{}
	svc := domainauth.NewService(store, mgr)

	if !svc.VerifyPassword("admin") {
		t.Fatal("expected default password to validate")
	}
	if !svc.MustChangePassword() {
		t.Fatal("expected default credentials to require change")
	}

	if err := svc.ChangePassword("admin", "Better!234"); err != nil {
		t.Fatalf("change password: %v", err)
	}
	if !svc.VerifyPassword("Better!234") {
		t.Fatal("expected updated password to validate")
	}
	if svc.MustChangePassword() {
		t.Fatal("expected password change requirement to be cleared")
	}
	if svc.PasswordRequirements() == "" {
		t.Fatal("expected password requirements string")
	}
}

func TestServiceCreateSession(t *testing.T) {
	mgr := &stubSessionManager{
		createToken:    "tok",
		createExpires:  time.Now().Add(time.Hour),
		validateExpiry: time.Now().Add(2 * time.Hour),
	}
	svc := domainauth.NewService(nil, mgr)

	token, expires, err := svc.CreateSession()
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if token != "tok" {
		t.Fatalf("expected token 'tok', got %q", token)
	}
	if expires.IsZero() {
		t.Fatal("expected expiry timestamp")
	}

	mgr.validateOK = true
	ok, refreshed := svc.ValidateSession(token)
	if !ok {
		t.Fatal("expected validation to succeed")
	}
	if mgr.lastValidated != token {
		t.Fatalf("expected validate to be called with %q, got %q", token, mgr.lastValidated)
	}
	if refreshed.IsZero() {
		t.Fatal("expected refreshed expiry timestamp")
	}

	svc.DeleteSession(token)
	if len(mgr.deleted) != 1 || mgr.deleted[0] != token {
		t.Fatalf("expected delete to be called with %q, got %#v", token, mgr.deleted)
	}
}

func TestServiceCreateSessionError(t *testing.T) {
	svc := domainauth.NewService(nil, &stubSessionManager{})
	if _, _, err := svc.CreateSession(); err == nil {
		t.Fatal("expected error when session manager returns empty token")
	}
}

func TestServiceCookieHelpers(t *testing.T) {
	svc := domainauth.NewService(nil, &stubSessionManager{})
	w := httptest.NewRecorder()
	expires := time.Now().Add(time.Hour)

	svc.SetSessionCookie(w, "tok", expires, true)
	resp := w.Result()
	cookies := resp.Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected one cookie, got %d", len(cookies))
	}
	if cookies[0].Name != "lp_session" {
		t.Fatalf("unexpected cookie name: %q", cookies[0].Name)
	}

	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(cookies[0])
	if got := svc.ReadSessionToken(req); got != "tok" {
		t.Fatalf("expected token 'tok', got %q", got)
	}

	w2 := httptest.NewRecorder()
	svc.ClearSessionCookie(w2)
	cleared := w2.Result().Cookies()
	if len(cleared) != 1 || cleared[0].MaxAge != -1 {
		t.Fatalf("expected clearing cookie to set MaxAge -1, got %#v", cleared)
	}
}
