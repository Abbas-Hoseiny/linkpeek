package utils

import (
	"fmt"
	"net/http"
	neturl "net/url"
	"strings"
	"time"
)

// NowUTC returns the current time in UTC.
func NowUTC() time.Time {
	return time.Now().UTC()
}

// ReqID generates a unique request ID based on the provided start time.
func ReqID(start time.Time) string {
	return fmt.Sprintf("r-%d", start.UnixNano())
}

// RemoteIP extracts the client IP address from an HTTP request.
// It honors common proxy headers like CF-Connecting-IP, X-Forwarded-For,
// Forwarded, and X-Real-IP, falling back to RemoteAddr if none are present.
func RemoteIP(r *http.Request) string {
	// honor common proxy headers
	if cf := r.Header.Get("CF-Connecting-IP"); cf != "" {
		return strings.Trim(strings.TrimSpace(cf), "[]")
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		return strings.Trim(strings.TrimSpace(parts[0]), "[]")
	}
	if fwd := r.Header.Get("Forwarded"); fwd != "" {
		// Rough parse of Forwarded: for=1.2.3.4;proto=https;host=...
		lower := strings.ToLower(fwd)
		for _, seg := range strings.Split(lower, ",") {
			for _, kv := range strings.Split(strings.TrimSpace(seg), ";") {
				kv = strings.TrimSpace(kv)
				if strings.HasPrefix(kv, "for=") {
					v := strings.TrimPrefix(kv, "for=")
					v = strings.Trim(v, "\"')")
					v = strings.Trim(v, "[]")
					if v != "" {
						return v
					}
				}
			}
		}
	}
	if xr := r.Header.Get("X-Real-IP"); xr != "" {
		return strings.Trim(strings.TrimSpace(xr), "[]")
	}
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i >= 0 {
		host = host[:i]
	}
	return strings.Trim(host, "[]")
}

// HeaderMap extracts relevant headers from an HTTP request into a map.
// It includes common headers used for tracking, security, and client hints.
func HeaderMap(r *http.Request) map[string]string {
	return map[string]string{
		"accept":                      r.Header.Get("Accept"),
		"accept-encoding":             r.Header.Get("Accept-Encoding"),
		"accept-language":             r.Header.Get("Accept-Language"),
		"referer":                     r.Header.Get("Referer"),
		"origin":                      r.Header.Get("Origin"),
		"host":                        r.Host,
		"cf-connecting-ip":            r.Header.Get("CF-Connecting-IP"),
		"x-forwarded-for":             r.Header.Get("X-Forwarded-For"),
		"x-forwarded-proto":           r.Header.Get("X-Forwarded-Proto"),
		"x-forwarded-host":            r.Header.Get("X-Forwarded-Host"),
		"x-forwarded-port":            r.Header.Get("X-Forwarded-Port"),
		"x-real-ip":                   r.Header.Get("X-Real-IP"),
		"forwarded":                   r.Header.Get("Forwarded"),
		"via":                         r.Header.Get("Via"),
		"sec-ch-ua":                   r.Header.Get("Sec-CH-UA"),
		"sec-ch-ua-mobile":            r.Header.Get("Sec-CH-UA-Mobile"),
		"sec-ch-ua-platform":          r.Header.Get("Sec-CH-UA-Platform"),
		"sec-ch-ua-model":             r.Header.Get("Sec-CH-UA-Model"),
		"sec-ch-ua-full-version-list": r.Header.Get("Sec-CH-UA-Full-Version-List"),
		"sec-fetch-site":              r.Header.Get("Sec-Fetch-Site"),
		"sec-fetch-mode":              r.Header.Get("Sec-Fetch-Mode"),
		"sec-fetch-user":              r.Header.Get("Sec-Fetch-User"),
		"sec-fetch-dest":              r.Header.Get("Sec-Fetch-Dest"),
		"dnt":                         r.Header.Get("DNT"),
		"viewport-width":              r.Header.Get("Viewport-Width"),
		"device-memory":               r.Header.Get("Device-Memory"),
		"downlink":                    r.Header.Get("Downlink"),
		"ect":                         r.Header.Get("ECT"),
		"rtt":                         r.Header.Get("RTT"),
		"save-data":                   r.Header.Get("Save-Data"),
	}
}

// ContentDisposition formats a Content-Disposition header value using RFC 8187 encoding.
func ContentDisposition(kind, name string) string {
	if name == "" {
		return kind
	}
	clean := strings.ReplaceAll(name, "\"", "")
	escaped := neturl.PathEscape(clean)
	return fmt.Sprintf("%s; filename=\"%s\"; filename*=UTF-8''%s", kind, clean, escaped)
}
