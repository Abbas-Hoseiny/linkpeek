package utils

import (
	"strings"
	"testing"
)

func TestValidateURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{
			name:    "valid http URL",
			url:     "http://example.com",
			wantErr: false,
		},
		{
			name:    "valid https URL",
			url:     "https://example.com/path?query=value",
			wantErr: false,
		},
		{
			name:    "URL too long",
			url:     "http://example.com/" + string(make([]byte, 2050)),
			wantErr: true,
		},
		{
			name:    "invalid scheme - ftp",
			url:     "ftp://example.com",
			wantErr: true,
		},
		{
			name:    "invalid scheme - javascript",
			url:     "javascript:alert(1)",
			wantErr: true,
		},
		{
			name:    "invalid scheme - data",
			url:     "data:text/html,<script>alert(1)</script>",
			wantErr: true,
		},
		{
			name:    "invalid URL format",
			url:     "not a url",
			wantErr: true,
		},
		{
			name:    "empty URL",
			url:     "",
			wantErr: true,
		},
		{
			name:    "valid URL near max length",
			url:     "https://example.com/path?q=" + strings.Repeat("a", 1990),
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateURL(tt.url)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateURL() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestIsValidURLScheme(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want bool
	}{
		{
			name: "http scheme",
			url:  "http://example.com",
			want: true,
		},
		{
			name: "https scheme",
			url:  "https://example.com",
			want: true,
		},
		{
			name: "HTTP uppercase",
			url:  "HTTP://example.com",
			want: true,
		},
		{
			name: "HTTPS uppercase",
			url:  "HTTPS://example.com",
			want: true,
		},
		{
			name: "ftp scheme",
			url:  "ftp://example.com",
			want: false,
		},
		{
			name: "javascript scheme",
			url:  "javascript:alert(1)",
			want: false,
		},
		{
			name: "file scheme",
			url:  "file:///etc/passwd",
			want: false,
		},
		{
			name: "invalid URL",
			url:  "not a url",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsValidURLScheme(tt.url); got != tt.want {
				t.Errorf("IsValidURLScheme() = %v, want %v", got, tt.want)
			}
		})
	}
}
