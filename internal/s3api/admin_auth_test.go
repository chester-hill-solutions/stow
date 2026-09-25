package s3api_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/chester-hill-solutions/stow-s3/internal/s3api"
	"github.com/chester-hill-solutions/stow-s3/internal/storage"
)

const testAdminToken = "s3-admin-token-for-tests"

// adminServer builds a server with the given admin configuration.
func adminServer(t *testing.T, cfg s3api.Config) *httptest.Server {
	t.Helper()
	if cfg.Auth == nil {
		cfg.Auth = s3api.DevBypass
	}
	if cfg.Host == "" {
		cfg.Host = "127.0.0.1"
	}
	if cfg.Store == nil {
		cfg.Store = storage.NewMemoryStore()
	}
	srv, err := s3api.New(cfg)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func getHeader(t *testing.T, url string, header, value string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if header != "" {
		req.Header.Set(header, value)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get %s: %v", url, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// AllowPublicAdmin used to be the only gate, so passing it exposed the outbox
// retry and discard actions to any client that could reach the port, with no
// credential. It is now ignored: a boolean cannot be the thing that authorizes.
func TestAllowPublicAdminAloneGrantsNothing(t *testing.T) {
	// httptest serves on loopback, so the remote case is dispatched synthetically
	// with a non-loopback RemoteAddr: that is the only thing the flag used to change.
	remote := httptest.NewRequest(http.MethodGet, "http://stow.example/_stow/status", nil)
	remote.RemoteAddr = "203.0.113.7:51000"
	if got := adminStatusFor(t, remote, s3api.Config{AllowPublicAdmin: true}); got != http.StatusNotFound {
		t.Fatalf("remote status with AllowPublicAdmin = %d, want 404", got)
	}

	ts := adminServer(t, s3api.Config{AllowPublicAdmin: true})
	if got := getHeader(t, ts.URL+"/_stow/health", "", "").StatusCode; got != http.StatusOK {
		t.Fatalf("loopback health = %d, want 200", got)
	}
}

// adminStatusFor dispatches a synthetic request through a server with the given
// config, so a non-loopback RemoteAddr can be exercised without a second host.
func adminStatusFor(t *testing.T, r *http.Request, cfg s3api.Config) int {
	t.Helper()
	srv, err := s3api.New(s3api.Config{
		Store:            storage.NewMemoryStore(),
		Auth:             s3api.DevBypass,
		Host:             "127.0.0.1",
		AllowPublicAdmin: cfg.AllowPublicAdmin,
		AdminToken:       cfg.AdminToken,
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, r)
	return rec.Code
}

// The token is the single mechanism. Read-only routes need it off loopback.
func TestReadOnlyAdminRoutesNeedTokenOffLoopback(t *testing.T) {
	readOnly := []string{"/_stow/status", "/_stow/inspect", "/_stow/metrics", "/_stow/health"}
	for _, path := range readOnly {
		t.Run(path, func(t *testing.T) {
			remote := httptest.NewRequest(http.MethodGet, "http://stow.example"+path, nil)
			remote.RemoteAddr = "203.0.113.7:51000"

			if got := adminStatusFor(t, remote, s3api.Config{}); got != http.StatusNotFound {
				t.Fatalf("no token = %d, want 404", got)
			}
			if got := adminStatusFor(t, remote, s3api.Config{AllowPublicAdmin: true}); got != http.StatusNotFound {
				t.Fatalf("AllowPublicAdmin without token = %d, want 404", got)
			}

			withToken := httptest.NewRequest(http.MethodGet, "http://stow.example"+path, nil)
			withToken.RemoteAddr = "203.0.113.7:51000"
			withToken.Header.Set(s3api.AdminTokenHeader, testAdminToken)
			if got := adminStatusFor(t, withToken, s3api.Config{AdminToken: testAdminToken}); got != http.StatusOK {
				t.Fatalf("with token = %d, want 200", got)
			}
		})
	}
}

// Destructive routes require the token even on loopback, because they change
// what reaches a live provider. Loopback is not a privilege boundary.
func TestDestructiveAdminRoutesRequireToken(t *testing.T) {
	ts := adminServer(t, s3api.Config{})
	for _, path := range []string{"/_stow/outbox/retry", "/_stow/outbox/discard"} {
		resp, err := http.Post(ts.URL+path, "application/json", nil)
		if err != nil {
			t.Fatalf("post %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("loopback %s without token = %d, want 404", path, resp.StatusCode)
		}
	}
}

// A wrong token must not authorize, and an empty header must not stand in for
// an unset token.
func TestWrongOrEmptyTokenDoesNotAuthorize(t *testing.T) {
	// Off loopback, so the loopback allowance cannot mask a token check.
	// Stray surrounding whitespace is deliberately tolerated: a caller that
	// still had to know the token has authorized itself, and header values
	// pick up trailing spaces easily.
	for _, presented := range []string{"", "wrong", testAdminToken + "x", testAdminToken[:len(testAdminToken)-1]} {
		remote := httptest.NewRequest(http.MethodGet, "http://stow.example/_stow/status", nil)
		remote.RemoteAddr = "203.0.113.7:51000"
		remote.Header.Set(s3api.AdminTokenHeader, presented)
		if got := adminStatusFor(t, remote, s3api.Config{AdminToken: testAdminToken}); got == http.StatusOK {
			t.Fatalf("token %q authorized off loopback", presented)
		}
	}

	// The same must hold on loopback for a destructive route, which has no
	// loopback allowance at all.
	ts := adminServer(t, s3api.Config{AdminToken: testAdminToken})
	for _, presented := range []string{"", "wrong", testAdminToken + "x"} {
		post, err := http.NewRequest(http.MethodPost, ts.URL+"/_stow/outbox/retry", nil)
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		post.Header.Set(s3api.AdminTokenHeader, presented)
		got, err := http.DefaultClient.Do(post)
		if err != nil {
			t.Fatalf("retry: %v", err)
		}
		got.Body.Close()
		if got.StatusCode == http.StatusOK {
			t.Fatalf("token %q authorized a destructive route on loopback", presented)
		}
	}

	// A server with no token configured must never authorize on an empty header.
	remote := httptest.NewRequest(http.MethodGet, "http://stow.example/_stow/status", nil)
	remote.RemoteAddr = "203.0.113.7:51000"
	remote.Header.Set(s3api.AdminTokenHeader, "")
	if got := adminStatusFor(t, remote, s3api.Config{}); got != http.StatusNotFound {
		t.Fatalf("status with no token configured = %d, want 404", got)
	}
}

// The pre-existing reflection bug: any website a developer visited could read
// their local bucket, with the credentials the SDK had already put in the page.
func TestCORSDoesNotReflectArbitraryOrigin(t *testing.T) {
	ts := adminServer(t, s3api.Config{})

	evil := getHeader(t, ts.URL, "Origin", "https://attacker.example")
	if got := evil.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("Access-Control-Allow-Origin = %q for a disallowed origin, want empty", got)
	}
	if vary := evil.Header.Get("Vary"); vary == "" {
		t.Fatal("response is missing Vary: Origin, so a cache can serve one origin's header to another")
	}
}

func TestCORSAllowsLoopbackOriginByDefault(t *testing.T) {
	ts := adminServer(t, s3api.Config{})
	for _, origin := range []string{"http://localhost:5173", "http://127.0.0.1:3000"} {
		resp := getHeader(t, ts.URL, "Origin", origin)
		if got := resp.Header.Get("Access-Control-Allow-Origin"); got != origin {
			t.Errorf("loopback origin %q got %q, want it echoed", origin, got)
		}
	}
}

func TestCORSExplicitAllowlistIsHonored(t *testing.T) {
	ts := adminServer(t, s3api.Config{CORSOrigins: []string{"https://app.example"}})

	allowed := getHeader(t, ts.URL, "Origin", "https://app.example")
	if got := allowed.Header.Get("Access-Control-Allow-Origin"); got != "https://app.example" {
		t.Fatalf("configured origin got %q, want it echoed", got)
	}
	// A configured allowlist replaces the loopback default rather than adding to it.
	loopback := getHeader(t, ts.URL, "Origin", "http://localhost:5173")
	if got := loopback.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("loopback origin got %q under a restrictive allowlist, want empty", got)
	}
}

func TestCORSPreflightRefusesDisallowedOrigin(t *testing.T) {
	ts := adminServer(t, s3api.Config{})
	req, err := http.NewRequest(http.MethodOptions, ts.URL, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Origin", "https://attacker.example")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("preflight status = %d, want 403", resp.StatusCode)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("refused preflight still returned Access-Control-Allow-Origin = %q", got)
	}
}
