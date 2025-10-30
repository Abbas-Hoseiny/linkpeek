package utils

import (
	"fmt"
	neturl "net/url"
	"strings"
)

// ValidateURL checks if a URL is valid and meets security requirements.
// It ensures the URL is not too long and uses only http or https schemes.
func ValidateURL(rawURL string) error {
	const maxURLLength = 2048

	if len(rawURL) > maxURLLength {
		return fmt.Errorf("URL too long (max %d characters)", maxURLLength)
	}

	u, err := neturl.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}

	// Only allow http and https schemes
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("invalid URL scheme: %s (only http and https allowed)", u.Scheme)
	}

	return nil
}

// IsValidURLScheme checks if a URL has an allowed scheme (http or https).
func IsValidURLScheme(rawURL string) bool {
	u, err := neturl.Parse(rawURL)
	if err != nil {
		return false
	}
	scheme := strings.ToLower(u.Scheme)
	return scheme == "http" || scheme == "https"
}
