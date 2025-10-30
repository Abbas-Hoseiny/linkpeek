package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"linkpeek/handlers"
	domainretry "linkpeek/internal/domain/retry"
)

func newTestRetryHandlers() *handlers.RetryLabHandlers {
	lab := domainretry.NewLab(nil)
	lab.RecordHit("retry-hint", "1.1.1.1")
	lab.RecordHit("retry-hint", "2.2.2.2")
	lab.RecordHit("drop-after-n", "3.3.3.3")
	return handlers.NewRetryLabHandlers(lab, retryLabHeader)
}

func TestRetryLabScenariosHandler(t *testing.T) {
	h := newTestRetryHandlers()
	req := httptest.NewRequest(http.MethodGet, "/api/retrylab/scenarios", nil)
	rr := httptest.NewRecorder()

	h.Scenarios(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var body struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(body.Items) != 3 {
		t.Fatalf("expected 3 scenarios, got %d", len(body.Items))
	}
}

func TestRetryLabStatsHandler(t *testing.T) {
	lab := domainretry.NewLab(nil)
	lab.RecordHit("retry-hint", "1.2.3.4")
	lab.RecordHit("retry-hint", "1.2.3.4")
	lab.RecordHit("retry-hint", "5.6.7.8")
	h := handlers.NewRetryLabHandlers(lab, retryLabHeader)

	req := httptest.NewRequest(http.MethodGet, "/api/retrylab/stats", nil)
	rr := httptest.NewRecorder()

	h.Stats(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var body struct {
		Items []domainretry.StatDTO `json:"items"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode stats body: %v", err)
	}
	if len(body.Items) == 0 {
		t.Fatalf("expected at least one stat entry")
	}
	found := false
	for _, item := range body.Items {
		if item.ID == "retry-hint" {
			found = true
			if item.TotalHits != 3 {
				t.Fatalf("expected 3 hits, got %d", item.TotalHits)
			}
			if item.UniqueIPs != 2 {
				t.Fatalf("expected 2 unique IPs, got %d", item.UniqueIPs)
			}
			if item.LastSeen == nil || time.Since(*item.LastSeen) > time.Second {
				t.Fatalf("unexpected last seen: %v", item.LastSeen)
			}
		}
	}
	if !found {
		t.Fatalf("retry-hint stats not found")
	}
}

func TestRetryLabRetryHintHandler(t *testing.T) {
	h := newTestRetryHandlers()
	req := httptest.NewRequest(http.MethodGet, "/retrylab/retry-hint", nil)
	req.RemoteAddr = "9.9.9.9:1234"
	rr := httptest.NewRecorder()

	h.RetryHint(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rr.Code)
	}
	if got := rr.Header().Get("Retry-After"); got != "5" {
		t.Fatalf("expected Retry-After 5, got %s", got)
	}
	if req.Header.Get(retryLabHeader) != "retry-hint" {
		t.Fatalf("expected header %s set", retryLabHeader)
	}
}

func TestRetryLabDropAfterNHandler(t *testing.T) {
	h := newTestRetryHandlers()
	req := httptest.NewRequest(http.MethodGet, "/retrylab/drop-after-n", nil)
	req.RemoteAddr = "8.8.8.8:4444"
	rr := httptest.NewRecorder()

	h.DropAfterN(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if rr.Body.Len() == 0 {
		t.Fatalf("expected partial body")
	}
}

func TestRetryLabWrongLengthHandler(t *testing.T) {
	h := newTestRetryHandlers()
	req := httptest.NewRequest(http.MethodGet, "/retrylab/wrong-length", nil)
	req.RemoteAddr = "7.7.7.7:4321"
	rr := httptest.NewRecorder()

	h.WrongLength(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if got := rr.Header().Get("Content-Length"); got != "1024" {
		t.Fatalf("expected Content-Length 1024, got %s", got)
	}
	if rr.Body.Len() >= 1024 {
		t.Fatalf("body should be shorter than advertised length")
	}
}

func TestRetryLabResetHandler(t *testing.T) {
	lab := domainretry.NewLab(nil)
	lab.RecordHit("retry-hint", "1.1.1.1")
	lab.RecordHit("drop-after-n", "2.2.2.2")

	h := handlers.NewRetryLabHandlers(lab, retryLabHeader)

	body := `{"ids":["retry-hint"]}`
	req := httptest.NewRequest(http.MethodPost, "/api/retrylab/reset", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	h.Reset(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var out struct {
		Items []domainretry.StatDTO `json:"items"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	var hint, drop *domainretry.StatDTO
	for i := range out.Items {
		switch out.Items[i].ID {
		case "retry-hint":
			hint = &out.Items[i]
		case "drop-after-n":
			drop = &out.Items[i]
		}
	}
	if hint == nil || drop == nil {
		t.Fatalf("expected stats for both scenarios in response")
	}
	if hint.TotalHits != 0 || hint.UniqueIPs != 0 || hint.LastSeen != nil {
		t.Fatalf("retry-hint stats not reset: %+v", hint)
	}
	if drop.TotalHits == 0 {
		t.Fatalf("drop-after-n should retain stats")
	}
}
