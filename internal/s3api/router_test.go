package s3api

import (
	"net/http/httptest"
	"testing"

	"github.com/chester-hill-solutions/stow-s3/internal/storage"
)

func TestNewUsesConfiguredBaseHost(t *testing.T) {
	srv, err := New(Config{
		Store:    storage.NewMemoryStore(),
		Auth:     DevBypass,
		Host:     "127.0.0.1",
		BaseHost: "localhost",
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	if srv.baseHost != "localhost" {
		t.Fatalf("base host = %q, want localhost", srv.baseHost)
	}
}

func TestAdminRoutesRejectRemoteRequestsByDefault(t *testing.T) {
	srv, err := New(Config{
		Store: storage.NewMemoryStore(),
		Auth:  DevBypass,
		Host:  "127.0.0.1",
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	req := httptest.NewRequest("GET", "http://localhost/_stow/status", nil)
	req.RemoteAddr = "203.0.113.10:4321"
	res := httptest.NewRecorder()
	srv.ServeHTTP(res, req)
	if res.Code != 404 {
		t.Fatalf("remote admin status = %d, want 404", res.Code)
	}
}

func TestAdminRoutesRejectMissingRemoteAddress(t *testing.T) {
	srv, err := New(Config{
		Store: storage.NewMemoryStore(),
		Auth:  DevBypass,
		Host:  "127.0.0.1",
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	req := httptest.NewRequest("GET", "http://localhost/_stow/status", nil)
	req.RemoteAddr = ""
	res := httptest.NewRecorder()
	srv.ServeHTTP(res, req)
	if res.Code != 404 {
		t.Fatalf("admin status without remote address = %d, want 404", res.Code)
	}
}

func TestParseRouteDecodesPathOnce(t *testing.T) {
	req := httptest.NewRequest("GET", "http://localhost/bucket/a%252Fb.txt", nil)
	route, err := parseRoute(req, "localhost")
	if err.Code != "" {
		t.Fatalf("route error = %+v", err)
	}
	if route.key != "a%2Fb.txt" {
		t.Fatalf("key = %q, want a%%2Fb.txt", route.key)
	}
}

func TestParseRouteVirtualHostedStyle(t *testing.T) {
	req := httptest.NewRequest("GET", "http://bucket.localhost:9000/a%2Fb.txt", nil)
	req.Host = "bucket.localhost:9000"
	route, err := parseRoute(req, "localhost")
	if err.Code != "" {
		t.Fatalf("route error = %+v", err)
	}
	if route.bucket != "bucket" || route.key != "a/b.txt" {
		t.Fatalf("route = %+v, want bucket/key", route)
	}
}
