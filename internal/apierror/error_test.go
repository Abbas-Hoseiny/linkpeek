package apierror

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWriteError(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		code       string
		message    string
		details    any
		wantStatus int
		wantCode   string
		wantMsg    string
	}{
		{
			name:       "simple error without details",
			status:     http.StatusBadRequest,
			code:       "invalid_request",
			message:    "The request is invalid",
			details:    nil,
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_request",
			wantMsg:    "The request is invalid",
		},
		{
			name:       "error with string details",
			status:     http.StatusInternalServerError,
			code:       "server_error",
			message:    "An internal error occurred",
			details:    "database connection failed",
			wantStatus: http.StatusInternalServerError,
			wantCode:   "server_error",
			wantMsg:    "An internal error occurred",
		},
		{
			name:    "error with map details",
			status:  http.StatusUnprocessableEntity,
			code:    "validation_error",
			message: "Validation failed",
			details: map[string]string{
				"field": "email",
				"issue": "invalid format",
			},
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   "validation_error",
			wantMsg:    "Validation failed",
		},
		{
			name:       "service unavailable error",
			status:     http.StatusServiceUnavailable,
			code:       "db_unavailable",
			message:    "Database is unavailable",
			details:    nil,
			wantStatus: http.StatusServiceUnavailable,
			wantCode:   "db_unavailable",
			wantMsg:    "Database is unavailable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a test response recorder
			w := httptest.NewRecorder()

			// Call WriteError
			WriteError(w, tt.status, tt.code, tt.message, tt.details)

			// Check status code
			if w.Code != tt.wantStatus {
				t.Errorf("WriteError() status = %v, want %v", w.Code, tt.wantStatus)
			}

			// Check Content-Type header
			contentType := w.Header().Get("Content-Type")
			if contentType != "application/json" {
				t.Errorf("WriteError() Content-Type = %v, want application/json", contentType)
			}

			// Parse response body
			var resp ErrorResponse
			if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
				t.Fatalf("Failed to decode response: %v", err)
			}

			// Check response fields
			if resp.Code != tt.wantCode {
				t.Errorf("WriteError() code = %v, want %v", resp.Code, tt.wantCode)
			}
			if resp.Message != tt.wantMsg {
				t.Errorf("WriteError() message = %v, want %v", resp.Message, tt.wantMsg)
			}

			// Check details if provided
			if tt.details != nil {
				if resp.Details == nil {
					t.Errorf("WriteError() details = nil, want %v", tt.details)
				}
			}
		})
	}
}

func TestWriteErrorContentType(t *testing.T) {
	w := httptest.NewRecorder()
	WriteError(w, http.StatusBadRequest, "test_error", "Test message", nil)

	contentType := w.Header().Get("Content-Type")
	if contentType != "application/json" {
		t.Errorf("Content-Type = %v, want application/json", contentType)
	}
}

func TestWriteErrorEmptyDetails(t *testing.T) {
	w := httptest.NewRecorder()
	WriteError(w, http.StatusBadRequest, "test_error", "Test message", nil)

	var resp ErrorResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	// When details is nil, the omitempty tag should exclude it from the JSON
	// But the field itself should exist in the struct
	if resp.Code != "test_error" {
		t.Errorf("Code = %v, want test_error", resp.Code)
	}
	if resp.Message != "Test message" {
		t.Errorf("Message = %v, want Test message", resp.Message)
	}
}
