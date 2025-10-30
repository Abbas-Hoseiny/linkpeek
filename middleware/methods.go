package middleware

import (
	"net/http"
	"strings"
)

// WithAllowedMethods returns middleware that enforces allowed HTTP methods.
// If the request method is not in the allowed list, it responds with 405 Method Not Allowed
// and sets the Allow header with the list of permitted methods.
func WithAllowedMethods(allowed ...string) func(http.Handler) http.Handler {
	// Normalize allowed methods to uppercase
	allowedMethods := make([]string, len(allowed))
	for i, method := range allowed {
		allowedMethods[i] = strings.ToUpper(method)
	}
	allowHeader := strings.Join(allowedMethods, ", ")

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Check if the request method is allowed
			requestMethod := strings.ToUpper(r.Method)
			for _, method := range allowedMethods {
				if requestMethod == method {
					next.ServeHTTP(w, r)
					return
				}
			}

			// Method not allowed
			w.Header().Set("Allow", allowHeader)
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		})
	}
}
