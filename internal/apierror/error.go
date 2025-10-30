package apierror

import (
	"encoding/json"
	"net/http"
)

// ErrorResponse represents the JSON structure for API errors
type ErrorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
}

// WriteError writes a JSON error response with the given status code, error code, message, and optional details.
// It automatically sets the Content-Type header to application/json.
func WriteError(w http.ResponseWriter, status int, code, message string, details any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	resp := ErrorResponse{
		Code:    code,
		Message: message,
		Details: details,
	}

	json.NewEncoder(w).Encode(resp)
}
