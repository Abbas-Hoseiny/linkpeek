package session_test

import (
	"net/http/httptest"
	"testing"
	"time"

	session "linkpeek/internal/domain/session"
)

func TestManagerCreateAndValidate(t *testing.T) {
	mgr := session.NewManager(50 * time.Millisecond)
	t.Cleanup(mgr.Close)

	token, expires := mgr.Create()
	if token == "" {
		t.Fatal("expected token to be generated")
	}
	if expires.IsZero() {
		t.Fatal("expected expiry timestamp to be set")
	}

	time.Sleep(5 * time.Millisecond)
	ok, refreshed := mgr.Validate(token)
	if !ok {
		t.Fatal("expected token to validate")
	}
	if !refreshed.After(expires) {
		t.Fatalf("expected expiry to be extended, got %v <= %v", refreshed, expires)
	}
}

func TestManagerValidateExpired(t *testing.T) {
	mgr := session.NewManager(10 * time.Millisecond)
	t.Cleanup(mgr.Close)

	token, _ := mgr.Create()
	if token == "" {
		t.Fatal("expected token to be generated")
	}

	time.Sleep(20 * time.Millisecond)
	if ok, _ := mgr.Validate(token); ok {
		t.Fatal("expected expired token to be rejected")
	}
}

func TestManagerDelete(t *testing.T) {
	mgr := session.NewManager(50 * time.Millisecond)
	t.Cleanup(mgr.Close)
	token, _ := mgr.Create()

	mgr.Delete(token)
	if ok, _ := mgr.Validate(token); ok {
		t.Fatal("expected deleted token to be invalid")
	}
}

func TestSessionCookieHelpers(t *testing.T) {
	w := httptest.NewRecorder()
	expires := time.Now().Add(time.Hour).Truncate(time.Second)

	session.SetSessionCookie(w, "tok", expires, true)
	resp := w.Result()
	cookies := resp.Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected one cookie, got %d", len(cookies))
	}
	cookie := cookies[0]
	if cookie.Name != "lp_session" || cookie.Value != "tok" {
		t.Fatalf("unexpected cookie: %#v", cookie)
	}
	if !cookie.HttpOnly || !cookie.Secure {
		t.Fatalf("expected secure httponly cookie, got %#v", cookie)
	}

	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(cookie)
	if got := session.ReadSessionToken(req); got != "tok" {
		t.Fatalf("expected token %q, got %q", "tok", got)
	}

	w2 := httptest.NewRecorder()
	session.ClearSessionCookie(w2)
	cleared := w2.Result().Cookies()
	if len(cleared) != 1 {
		t.Fatalf("expected one cookie after clear, got %d", len(cleared))
	}
	if cleared[0].MaxAge != -1 {
		t.Fatalf("expected clearing cookie to set MaxAge -1, got %d", cleared[0].MaxAge)
	}
}
