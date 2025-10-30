package handlers

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"runtime"
	"syscall"
)

// HealthHandler handles health check requests
type HealthHandler struct {
	DB      *sql.DB
	DataDir string
}

// NewHealthHandler creates a new HealthHandler
func NewHealthHandler(db *sql.DB, dataDir string) *HealthHandler {
	return &HealthHandler{
		DB:      db,
		DataDir: dataDir,
	}
}

// HealthResponse represents the enriched health check response
type HealthResponse struct {
	Status  string       `json:"status"`
	Checks  HealthChecks `json:"checks"`
	Runtime RuntimeInfo  `json:"runtime"`
}

// HealthChecks contains individual health check results
type HealthChecks struct {
	Database DiskCheck `json:"database"`
	Disk     DiskCheck `json:"disk"`
}

// DiskCheck represents a health check result
type DiskCheck struct {
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

// RuntimeInfo contains Go runtime information
type RuntimeInfo struct {
	GoVersion string `json:"go_version"`
}

// ServeHTTP handles the /healthz endpoint
func (h *HealthHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	response := HealthResponse{
		Status: "healthy",
		Checks: HealthChecks{
			Database: h.checkDatabase(),
			Disk:     h.checkDisk(),
		},
		Runtime: RuntimeInfo{
			GoVersion: runtime.Version(),
		},
	}

	// If any check fails, set overall status to unhealthy
	if response.Checks.Database.Status != "ok" || response.Checks.Disk.Status != "ok" {
		response.Status = "unhealthy"
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(response)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(response)
}

// checkDatabase checks if the database connection is healthy
func (h *HealthHandler) checkDatabase() DiskCheck {
	if h.DB == nil {
		return DiskCheck{
			Status:  "ok",
			Message: "no database configured",
		}
	}

	if err := h.DB.Ping(); err != nil {
		return DiskCheck{
			Status:  "error",
			Message: "database ping failed: " + err.Error(),
		}
	}

	return DiskCheck{
		Status:  "ok",
		Message: "database connection healthy",
	}
}

// checkDisk checks if the data directory has sufficient disk space
func (h *HealthHandler) checkDisk() DiskCheck {
	if h.DataDir == "" {
		return DiskCheck{
			Status:  "ok",
			Message: "no data directory configured",
		}
	}

	// Check if the directory exists
	info, err := os.Stat(h.DataDir)
	if err != nil {
		if os.IsNotExist(err) {
			return DiskCheck{
				Status:  "error",
				Message: "data directory does not exist",
			}
		}
		return DiskCheck{
			Status:  "error",
			Message: "failed to check data directory: " + err.Error(),
		}
	}

	if !info.IsDir() {
		return DiskCheck{
			Status:  "error",
			Message: "data path is not a directory",
		}
	}

	// Get disk space information
	var stat syscall.Statfs_t
	if err := syscall.Statfs(h.DataDir, &stat); err != nil {
		return DiskCheck{
			Status:  "error",
			Message: "failed to get disk stats: " + err.Error(),
		}
	}

	// Calculate available space in bytes
	availableBytes := stat.Bavail * uint64(stat.Bsize)
	// Check if at least 100MB is available
	minRequiredBytes := uint64(100 * 1024 * 1024)

	if availableBytes < minRequiredBytes {
		return DiskCheck{
			Status:  "error",
			Message: "insufficient disk space available",
		}
	}

	return DiskCheck{
		Status:  "ok",
		Message: "sufficient disk space available",
	}
}
