package handlers

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestHealthHandler_Healthy(t *testing.T) {
	// Create a temporary directory for testing
	tmpDir, err := os.MkdirTemp("", "health-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a health handler with nil DB (as the current implementation doesn't use it)
	handler := NewHealthHandler(nil, tmpDir)

	// Create a test request
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()

	// Execute the handler
	handler.ServeHTTP(w, req)

	// Check status code
	if w.Code != http.StatusOK {
		t.Errorf("Expected status code %d, got %d", http.StatusOK, w.Code)
	}

	// Check Content-Type
	contentType := w.Header().Get("Content-Type")
	if contentType != "application/json" {
		t.Errorf("Expected Content-Type 'application/json', got '%s'", contentType)
	}

	// Parse and validate response structure
	var response HealthResponse
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if response.Status != "healthy" {
		t.Errorf("Expected status 'healthy', got '%s'", response.Status)
	}
}

// TestHealthDetails tests the enriched health response with detailed checks
func TestHealthDetails(t *testing.T) {
	// Create a temporary directory for testing
	tmpDir, err := os.MkdirTemp("", "health-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	handler := NewHealthHandler(nil, tmpDir)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status code %d, got %d", http.StatusOK, w.Code)
	}

	// Parse response
	var response HealthResponse
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	// Verify overall status
	if response.Status != "healthy" {
		t.Errorf("Expected status 'healthy', got '%s'", response.Status)
	}

	// Verify database check
	if response.Checks.Database.Status != "ok" {
		t.Errorf("Expected database status 'ok', got '%s'", response.Checks.Database.Status)
	}

	// Verify disk check
	if response.Checks.Disk.Status != "ok" {
		t.Errorf("Expected disk status 'ok', got '%s'", response.Checks.Disk.Status)
	}

	// Verify runtime info
	if response.Runtime.GoVersion == "" {
		t.Error("Expected GoVersion to be set")
	}

	expectedGoVersion := runtime.Version()
	if response.Runtime.GoVersion != expectedGoVersion {
		t.Errorf("Expected GoVersion '%s', got '%s'", expectedGoVersion, response.Runtime.GoVersion)
	}
}

// TestHealthDetails_DBFailure tests health check when database ping fails
func TestHealthDetails_DBFailure(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "health-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a DB with invalid connection to simulate failure
	db, err := sql.Open("pgx", "postgres://invalid:invalid@localhost:9999/invalid?connect_timeout=1")
	if err != nil {
		t.Fatalf("Failed to create mock DB: %v", err)
	}
	defer db.Close()

	handler := NewHealthHandler(db, tmpDir)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	// Should return 503 Service Unavailable when unhealthy
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("Expected status code %d, got %d", http.StatusServiceUnavailable, w.Code)
	}

	var response HealthResponse
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	// Overall status should be unhealthy
	if response.Status != "unhealthy" {
		t.Errorf("Expected status 'unhealthy', got '%s'", response.Status)
	}

	// Database check should be in error state
	if response.Checks.Database.Status != "error" {
		t.Errorf("Expected database status 'error', got '%s'", response.Checks.Database.Status)
	}

	// Error message should mention database ping failure
	if !strings.Contains(response.Checks.Database.Message, "database ping failed") {
		t.Errorf("Expected error message about database ping, got '%s'", response.Checks.Database.Message)
	}
}

// TestHealthDetails_MissingDataDir tests health check when data directory doesn't exist
func TestHealthDetails_MissingDataDir(t *testing.T) {
	// Use a non-existent directory path
	nonExistentDir := "/tmp/non-existent-dir-" + t.Name()

	// Ensure the directory doesn't exist
	os.RemoveAll(nonExistentDir)

	handler := NewHealthHandler(nil, nonExistentDir)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	// Should return 503 Service Unavailable
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("Expected status code %d, got %d", http.StatusServiceUnavailable, w.Code)
	}

	var response HealthResponse
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	// Overall status should be unhealthy
	if response.Status != "unhealthy" {
		t.Errorf("Expected status 'unhealthy', got '%s'", response.Status)
	}

	// Disk check should be in error state
	if response.Checks.Disk.Status != "error" {
		t.Errorf("Expected disk status 'error', got '%s'", response.Checks.Disk.Status)
	}

	// Error message should mention missing directory
	if !strings.Contains(response.Checks.Disk.Message, "does not exist") {
		t.Errorf("Expected error message about missing directory, got '%s'", response.Checks.Disk.Message)
	}
}

func TestHealthHandler_WithValidDataDir(t *testing.T) {
	// Create a temporary directory for testing
	tmpDir, err := os.MkdirTemp("", "health-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create some test files in the directory
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	handler := NewHealthHandler(nil, tmpDir)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status code %d, got %d", http.StatusOK, w.Code)
	}

	// Parse and validate the response has proper structure
	var response HealthResponse
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if response.Checks.Disk.Status != "ok" {
		t.Errorf("Expected disk status 'ok', got '%s'", response.Checks.Disk.Status)
	}
}

func TestHealthHandler_NilDB(t *testing.T) {
	// Test with nil DB to ensure handler doesn't panic
	tmpDir, err := os.MkdirTemp("", "health-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	handler := NewHealthHandler(nil, tmpDir)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status code %d, got %d", http.StatusOK, w.Code)
	}

	var response HealthResponse
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	// With nil DB, the database check should still be OK (no DB configured)
	if response.Checks.Database.Status != "ok" {
		t.Errorf("Expected database status 'ok' with nil DB, got '%s'", response.Checks.Database.Status)
	}
}

func TestHealthHandler_EmptyDataDir(t *testing.T) {
	// Test with empty string as dataDir
	handler := NewHealthHandler(nil, "")

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status code %d, got %d", http.StatusOK, w.Code)
	}

	var response HealthResponse
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	// With empty dataDir, disk check should be OK (no directory configured)
	if response.Checks.Disk.Status != "ok" {
		t.Errorf("Expected disk status 'ok' with empty dataDir, got '%s'", response.Checks.Disk.Status)
	}
}

func TestNewHealthHandler(t *testing.T) {
	// Test the constructor
	tmpDir := "/test/dir"
	var db *sql.DB // nil DB for testing

	handler := NewHealthHandler(db, tmpDir)

	if handler == nil {
		t.Fatal("NewHealthHandler returned nil")
	}

	if handler.DB != db {
		t.Errorf("Expected DB to be %v, got %v", db, handler.DB)
	}

	if handler.DataDir != tmpDir {
		t.Errorf("Expected DataDir to be %s, got %s", tmpDir, handler.DataDir)
	}
}

func TestHealthHandler_HTTPMethods(t *testing.T) {
	// Test that health endpoint responds to different HTTP methods
	tmpDir, err := os.MkdirTemp("", "health-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	handler := NewHealthHandler(nil, tmpDir)

	tests := []struct {
		name   string
		method string
	}{
		{"GET request", http.MethodGet},
		{"HEAD request", http.MethodHead},
		{"POST request", http.MethodPost},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, "/healthz", nil)
			w := httptest.NewRecorder()

			handler.ServeHTTP(w, req)

			// Health endpoint should respond to any method (current implementation)
			if w.Code != http.StatusOK {
				t.Errorf("Expected status code %d for %s, got %d", http.StatusOK, tt.method, w.Code)
			}
		})
	}
}
