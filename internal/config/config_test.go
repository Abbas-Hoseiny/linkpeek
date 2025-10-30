package config

import (
	"os"
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	// Save original env vars and restore after test
	originalPort := os.Getenv("PORT")
	originalDataDir := os.Getenv("DATA_DIR")
	defer func() {
		os.Setenv("PORT", originalPort)
		os.Setenv("DATA_DIR", originalDataDir)
	}()

	// Test defaults
	os.Unsetenv("PORT")
	os.Unsetenv("DATA_DIR")
	cfg := Load()

	if cfg.Port != "9009" {
		t.Errorf("Expected default port 9009, got %s", cfg.Port)
	}

	if cfg.DataDir != "/data" {
		t.Errorf("Expected default data dir /data, got %s", cfg.DataDir)
	}

	if cfg.RateLimitRequests != 100 {
		t.Errorf("Expected rate limit 100, got %d", cfg.RateLimitRequests)
	}

	if cfg.HTTPClientTimeout != 30*time.Second {
		t.Errorf("Expected HTTP timeout 30s, got %v", cfg.HTTPClientTimeout)
	}
}

func TestLoadWithEnvVars(t *testing.T) {
	// Save original env vars and restore after test
	originalPort := os.Getenv("PORT")
	originalDataDir := os.Getenv("DATA_DIR")
	originalRateLimit := os.Getenv("RATE_LIMIT_REQUESTS")
	defer func() {
		os.Setenv("PORT", originalPort)
		os.Setenv("DATA_DIR", originalDataDir)
		os.Setenv("RATE_LIMIT_REQUESTS", originalRateLimit)
	}()

	// Set custom values
	os.Setenv("PORT", "8080")
	os.Setenv("DATA_DIR", "/custom/data")
	os.Setenv("RATE_LIMIT_REQUESTS", "200")

	cfg := Load()

	if cfg.Port != "8080" {
		t.Errorf("Expected port 8080, got %s", cfg.Port)
	}

	if cfg.DataDir != "/custom/data" {
		t.Errorf("Expected data dir /custom/data, got %s", cfg.DataDir)
	}

	if cfg.RateLimitRequests != 200 {
		t.Errorf("Expected rate limit 200, got %d", cfg.RateLimitRequests)
	}
}

func TestGetEnvDuration(t *testing.T) {
	original := os.Getenv("TEST_DURATION")
	defer os.Setenv("TEST_DURATION", original)

	os.Setenv("TEST_DURATION", "5m")
	duration := getEnvDuration("TEST_DURATION", time.Second)

	if duration != 5*time.Minute {
		t.Errorf("Expected 5 minutes, got %v", duration)
	}

	// Test invalid duration falls back to default
	os.Setenv("TEST_DURATION", "invalid")
	duration = getEnvDuration("TEST_DURATION", 10*time.Second)

	if duration != 10*time.Second {
		t.Errorf("Expected default 10s for invalid duration, got %v", duration)
	}
}

func TestGetEnvBool(t *testing.T) {
	original := os.Getenv("TEST_BOOL")
	defer os.Setenv("TEST_BOOL", original)

	tests := []struct {
		name         string
		value        string
		defaultValue bool
		expected     bool
	}{
		{"true value", "true", false, true},
		{"false value", "false", true, false},
		{"1 value", "1", false, true},
		{"0 value", "0", true, false},
		{"invalid value", "invalid", true, true},
		{"empty value", "", false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.value == "" {
				os.Unsetenv("TEST_BOOL")
			} else {
				os.Setenv("TEST_BOOL", tt.value)
			}

			result := getEnvBool("TEST_BOOL", tt.defaultValue)
			if result != tt.expected {
				t.Errorf("Expected %v, got %v", tt.expected, result)
			}
		})
	}
}
