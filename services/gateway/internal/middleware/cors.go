package middleware

import (
	"net/http"
	"strings"
)

// CORS adds permissive-but-configurable CORS headers so the browser consoles
// (different origin) can call the gateway. allowedOrigins is a comma-separated
// list; "*" (the default when empty) echoes any origin. Pre-flight OPTIONS is
// answered here and short-circuits.
type CORS struct {
	origins map[string]bool
	any     bool
}

// NewCORS builds the CORS middleware. Pass an empty string to allow any origin.
func NewCORS(allowedOrigins string) *CORS {
	c := &CORS{origins: map[string]bool{}}
	allowedOrigins = strings.TrimSpace(allowedOrigins)
	if allowedOrigins == "" || allowedOrigins == "*" {
		c.any = true
		return c
	}
	for _, o := range strings.Split(allowedOrigins, ",") {
		if o = strings.TrimSpace(o); o != "" {
			c.origins[o] = true
		}
	}
	return c
}

// Wrap returns next with CORS handling applied.
func (c *CORS) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && (c.any || c.origins[origin]) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Connect-Protocol-Version")
			w.Header().Set("Access-Control-Max-Age", "86400")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
