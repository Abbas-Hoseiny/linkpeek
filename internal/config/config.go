package config

import (
	"os"
	"strconv"
	"time"
)

// Config holds all application configuration
type Config struct {
	// Server configuration
	Port string

	// Data directory
	DataDir string

	// Authentication
	SessionTTL time.Duration

	// Rate limiting
	RateLimitRequests int
	RateLimitWindow   time.Duration

	// Payload configuration
	PayloadMaxUploadMB int64

	// Snippet configuration
	SnippetMaxBytes int

	// HTTP client timeout
	HTTPClientTimeout time.Duration

	// Cloudflare configuration
	AllowTunnelAdmin     bool
	CloudflaredContainer string

	// Realtime WebSocket configuration
	RealtimeEnabled bool

	// Database configuration
	DatabaseURL string
}

// Load reads configuration from environment variables with sensible defaults
func Load() *Config {
	return &Config{
		Port:                 getEnv("PORT", "9009"),
		DataDir:              getEnv("DATA_DIR", "/data"),
		SessionTTL:           getEnvDuration("SESSION_TTL", 12*time.Hour),
		RateLimitRequests:    getEnvInt("RATE_LIMIT_REQUESTS", 100),
		RateLimitWindow:      getEnvDuration("RATE_LIMIT_WINDOW", time.Minute),
		PayloadMaxUploadMB:   getEnvInt64("PAYLOAD_MAX_UPLOAD_MB", 250),
		SnippetMaxBytes:      getEnvInt("SNIPPET_MAX_BYTES", 64*1024),
		HTTPClientTimeout:    getEnvDuration("HTTP_CLIENT_TIMEOUT", 30*time.Second),
		AllowTunnelAdmin:     getEnvBool("ALLOW_TUNNEL_ADMIN", false),
		CloudflaredContainer: getEnv("CLOUDFLARED_CONTAINER", ""),
		RealtimeEnabled:      getEnvBool("REALTIME_ENABLED", true),
		DatabaseURL:          getEnv("DATABASE_URL", ""),
	}
}

// getEnv retrieves a string environment variable or returns the default value
func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// getEnvInt retrieves an integer environment variable or returns the default value
func getEnvInt(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if i, err := strconv.Atoi(value); err == nil {
			return i
		}
	}
	return defaultValue
}

// getEnvInt64 retrieves an int64 environment variable or returns the default value
func getEnvInt64(key string, defaultValue int64) int64 {
	if value := os.Getenv(key); value != "" {
		if i, err := strconv.ParseInt(value, 10, 64); err == nil {
			return i
		}
	}
	return defaultValue
}

// getEnvBool retrieves a boolean environment variable or returns the default value
func getEnvBool(key string, defaultValue bool) bool {
	if value := os.Getenv(key); value != "" {
		if b, err := strconv.ParseBool(value); err == nil {
			return b
		}
	}
	return defaultValue
}

// getEnvDuration retrieves a duration environment variable or returns the default value
func getEnvDuration(key string, defaultValue time.Duration) time.Duration {
	if value := os.Getenv(key); value != "" {
		if d, err := time.ParseDuration(value); err == nil {
			return d
		}
	}
	return defaultValue
}
