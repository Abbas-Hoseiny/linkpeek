package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWithAllowedMethods(t *testing.T) {
	// Create a simple handler that returns 200 OK
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("success"))
	})

	tests := []struct {
		name            string
		allowedMethods  []string
		requestMethod   string
		wantStatus      int
		wantAllowHeader string
	}{
		{
			name:            "GET allowed with GET request",
			allowedMethods:  []string{"GET"},
			requestMethod:   "GET",
			wantStatus:      http.StatusOK,
			wantAllowHeader: "",
		},
		{
			name:            "POST allowed with POST request",
			allowedMethods:  []string{"POST"},
			requestMethod:   "POST",
			wantStatus:      http.StatusOK,
			wantAllowHeader: "",
		},
		{
			name:            "GET and POST allowed with GET request",
			allowedMethods:  []string{"GET", "POST"},
			requestMethod:   "GET",
			wantStatus:      http.StatusOK,
			wantAllowHeader: "",
		},
		{
			name:            "GET and POST allowed with POST request",
			allowedMethods:  []string{"GET", "POST"},
			requestMethod:   "POST",
			wantStatus:      http.StatusOK,
			wantAllowHeader: "",
		},
		{
			name:            "GET allowed with POST request (denied)",
			allowedMethods:  []string{"GET"},
			requestMethod:   "POST",
			wantStatus:      http.StatusMethodNotAllowed,
			wantAllowHeader: "GET",
		},
		{
			name:            "POST allowed with GET request (denied)",
			allowedMethods:  []string{"POST"},
			requestMethod:   "GET",
			wantStatus:      http.StatusMethodNotAllowed,
			wantAllowHeader: "POST",
		},
		{
			name:            "GET and POST allowed with DELETE request (denied)",
			allowedMethods:  []string{"GET", "POST"},
			requestMethod:   "DELETE",
			wantStatus:      http.StatusMethodNotAllowed,
			wantAllowHeader: "GET, POST",
		},
		{
			name:            "Multiple methods in Allow header",
			allowedMethods:  []string{"GET", "POST", "PUT", "PATCH"},
			requestMethod:   "DELETE",
			wantStatus:      http.StatusMethodNotAllowed,
			wantAllowHeader: "GET, POST, PUT, PATCH",
		},
		{
			name:            "Case insensitive method matching (lowercase allowed)",
			allowedMethods:  []string{"get", "post"},
			requestMethod:   "GET",
			wantStatus:      http.StatusOK,
			wantAllowHeader: "",
		},
		{
			name:            "Case insensitive method matching (mixed case allowed)",
			allowedMethods:  []string{"Get", "Post"},
			requestMethod:   "POST",
			wantStatus:      http.StatusOK,
			wantAllowHeader: "",
		},
		{
			name:            "HEAD method",
			allowedMethods:  []string{"GET", "HEAD"},
			requestMethod:   "HEAD",
			wantStatus:      http.StatusOK,
			wantAllowHeader: "",
		},
		{
			name:            "OPTIONS method",
			allowedMethods:  []string{"GET", "POST", "OPTIONS"},
			requestMethod:   "OPTIONS",
			wantStatus:      http.StatusOK,
			wantAllowHeader: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Wrap handler with middleware
			wrapped := WithAllowedMethods(tt.allowedMethods...)(handler)

			// Create test request
			req := httptest.NewRequest(tt.requestMethod, "/test", nil)
			rec := httptest.NewRecorder()

			// Execute request
			wrapped.ServeHTTP(rec, req)

			// Check status code
			if rec.Code != tt.wantStatus {
				t.Errorf("status code = %d, want %d", rec.Code, tt.wantStatus)
			}

			// Check Allow header when method is not allowed
			if tt.wantStatus == http.StatusMethodNotAllowed {
				allowHeader := rec.Header().Get("Allow")
				if allowHeader != tt.wantAllowHeader {
					t.Errorf("Allow header = %q, want %q", allowHeader, tt.wantAllowHeader)
				}
			}

			// Check response body for successful requests
			if tt.wantStatus == http.StatusOK {
				body := rec.Body.String()
				if body != "success" {
					t.Errorf("body = %q, want %q", body, "success")
				}
			}
		})
	}
}

func TestWithAllowedMethodsEmptyList(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// Create middleware with no allowed methods
	wrapped := WithAllowedMethods()(handler)

	req := httptest.NewRequest("GET", "/test", nil)
	rec := httptest.NewRecorder()

	wrapped.ServeHTTP(rec, req)

	// Should deny all requests when no methods are allowed
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status code = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}
