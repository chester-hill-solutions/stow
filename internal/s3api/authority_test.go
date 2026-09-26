package s3api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chester-hill-solutions/stow-s3/internal/authority"
	"github.com/chester-hill-solutions/stow-s3/internal/runtime"
	"github.com/chester-hill-solutions/stow-s3/internal/s3api"
	"github.com/chester-hill-solutions/stow-s3/internal/storage"
)

// A read-only environment, reached over S3. The store handed to the server is an
// adapter over a runtime Instance, so this exercises the same authority check a
// native caller would hit.
func readOnlyServer(t *testing.T) *httptest.Server {
	t.Helper()
	granted := authority.All().Without(authority.ObjectWrite, authority.ObjectDelete)

	store := storage.NewMemoryStore()
	if err := store.CreateBucket(context.Background(), "bucket"); err != nil {
		t.Fatalf("seed bucket: %v", err)
	}
	instance, err := runtime.OpenWithStore(runtime.Options{
		Backend:   runtime.BackendMemory,
		Authority: &granted,
	}, store, nil)
	if err != nil {
		t.Fatalf("open runtime: %v", err)
	}
	adapter, err := runtime.NewStoreAdapter(instance)
	if err != nil {
		t.Fatalf("adapter: %v", err)
	}
	srv, err := s3api.New(s3api.Config{
		Store: adapter,
		Auth:  s3api.DevBypass,
		Host:  "127.0.0.1",
		Port:  0,
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	httpServer := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		httpServer.Close()
		if err := instance.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	return httpServer
}

// A refusal must be 403 AccessDenied. Reporting it as 500 InternalError would
// tell an SDK the server broke and invite a retry of something that can never
// succeed, and would tell the caller nothing about why.
func TestS3RefusesAWriteWithAccessDeniedNotInternalError(t *testing.T) {
	server := readOnlyServer(t)

	req, err := http.NewRequest(http.MethodPut, server.URL+"/bucket/key", strings.NewReader("payload"))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
	body := make([]byte, 512)
	n, _ := resp.Body.Read(body)
	if !strings.Contains(string(body[:n]), "AccessDenied") {
		t.Errorf("body = %q, want an AccessDenied code", body[:n])
	}
	if strings.Contains(string(body[:n]), "InternalError") {
		t.Errorf("a permission decision was reported as a server fault: %q", body[:n])
	}
}

// The refusal must not have stored the object. A check that runs after the work
// is not a permission check.
func TestS3RefusalStoresNothing(t *testing.T) {
	server := readOnlyServer(t)

	req, _ := http.NewRequest(http.MethodPut, server.URL+"/bucket/key", strings.NewReader("payload"))
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	resp.Body.Close()

	head, _ := http.NewRequest(http.MethodHead, server.URL+"/bucket/key", nil)
	headResp, err := server.Client().Do(head)
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	defer headResp.Body.Close()

	if headResp.StatusCode != http.StatusNotFound {
		t.Errorf("after a refused PUT, HEAD = %d, want 404", headResp.StatusCode)
	}
}

// A restricted environment is still a usable one. Reads must keep working or the
// restriction is a denial of service rather than a permission.
func TestS3ReadsStillWorkUnderAReadOnlyAuthority(t *testing.T) {
	server := readOnlyServer(t)

	resp, err := server.Client().Get(server.URL + "/bucket?list-type=2")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("ListObjectsV2 = %d, want 200", resp.StatusCode)
	}
}

// Unrestricted behaviour must be untouched. This is the guard on the mapping
// itself: a new case in mapStorageError must not change any other error's code.
func TestAnUnrestrictedEnvironmentIsUnaffected(t *testing.T) {
	srv, err := s3api.New(s3api.Config{
		Store: storage.NewMemoryStore(),
		Auth:  s3api.DevBypass,
		Host:  "127.0.0.1",
		Port:  0,
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	server := httptest.NewServer(srv.Handler())
	defer server.Close()

	req, _ := http.NewRequest(http.MethodPut, server.URL+"/bucket/key", strings.NewReader("payload"))
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	defer resp.Body.Close()

	// NoSuchBucket, because nothing created the bucket — not AccessDenied.
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("PUT to a missing bucket = %d, want 404", resp.StatusCode)
	}
}
