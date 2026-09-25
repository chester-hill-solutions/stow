package s3api

import (
	"context"
	"net/http"
	"net/url"
	"strings"
)

// AdminTokenHeader carries the admin credential on admin routes. It is a
// stow-specific header rather than Authorization so it can never be confused
// with, or interfere with, a SigV4 signature on an S3 route.
const AdminTokenHeader = "X-Stow-Admin"

// wildcardCORSOrigin is the explicit opt-in to permitting any browser origin.
// It is only honored when an operator asks for it by name, because the previous
// behavior of reflecting whatever Origin arrived let any website a developer
// visited read their local bucket, including its contents, using the credentials
// the SDK had already placed in the page.
const wildcardCORSOrigin = "*"

// originAllowed reports whether a browser origin may read responses.
//
// With no configured allowlist the default is loopback origins, which is the
// case a local development server actually has: a browser app served from
// localhost talking to a stow on localhost. Anything else must be named.
func originAllowed(configured []string, origin string) bool {
	if origin == "" {
		// Not a browser request. CORS does not apply and nothing should be set.
		return false
	}
	if len(configured) == 0 {
		return isLoopbackOrigin(origin)
	}
	for _, allowed := range configured {
		allowed = strings.TrimSpace(allowed)
		if allowed == wildcardCORSOrigin {
			return true
		}
		if strings.EqualFold(allowed, origin) {
			return true
		}
	}
	return false
}

// isLoopbackOrigin reports whether an Origin header names a loopback host.
func isLoopbackOrigin(origin string) bool {
	parsed, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return false
	}
	host := parsed.Hostname()
	// url.Parse leaves a bare host without a port intact, and Hostname strips
	// the port when there is one, so a wildcard port is not needed here.
	return host == "localhost" || host == "127.0.0.1" || host == "::1" ||
		strings.HasSuffix(host, ".localhost")
}

// setCORS applies the response CORS headers.
//
// Vary: Origin is set unconditionally. Without it a shared cache or a browser
// cache can serve one origin's allow header to a different origin, which
// reintroduces the reflection this function exists to remove.
func setCORS(w http.ResponseWriter, r *http.Request) {
	w.Header().Add("Vary", "Origin")
	origin := r.Header.Get("Origin")
	if !originAllowed(corsOriginsFor(r), origin) {
		// Deliberately no Access-Control-Allow-Origin. The browser blocks the
		// read, which is the point; sending anything permissive here is the bug.
		return
	}
	w.Header().Set("Access-Control-Allow-Origin", origin)
	w.Header().Set("Access-Control-Expose-Headers", "ETag, x-amz-meta-*")
}

// handleCORSPreflight answers a preflight, refusing origins outside the
// allowlist rather than answering permissively and letting the browser decide.
func handleCORSPreflight(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodOptions {
		return false
	}
	w.Header().Add("Vary", "Origin")
	origin := r.Header.Get("Origin")
	if !originAllowed(corsOriginsFor(r), origin) {
		w.WriteHeader(http.StatusForbidden)
		return true
	}
	w.Header().Set("Access-Control-Allow-Origin", origin)
	w.Header().Set("Access-Control-Allow-Methods", "GET, PUT, POST, DELETE, HEAD")
	w.Header().Set("Access-Control-Allow-Headers", "*")
	w.Header().Set("Access-Control-Max-Age", "3000")
	w.WriteHeader(http.StatusOK)
	return true
}

// corsOriginsContextKey carries the server's allowlist on the request.
//
// The allowlist is server configuration, but setCORS and writeError are reached
// from free functions that only have the request. Attaching it to the context
// once in ServeHTTP avoids threading it through every error and XML helper.
type corsOriginsContextKey struct{}

func withCORSOrigins(r *http.Request, allowed []string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), corsOriginsContextKey{}, allowed))
}

func corsOriginsFor(r *http.Request) []string {
	allowed, _ := r.Context().Value(corsOriginsContextKey{}).([]string)
	return allowed
}

func isAdminPath(path string) bool {
	return strings.HasPrefix(path, "/_stow/")
}

// isDestructiveAdminPath reports whether an admin route changes durable state,
// most importantly the run-through outbox, whose discard and retry affect what
// is propagated to a live provider. These never run without the admin token.
func isDestructiveAdminPath(path string) bool {
	return path == "/_stow/outbox/retry" || path == "/_stow/outbox/discard"
}
