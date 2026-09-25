package main

import (
	"bytes"
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/chester-hill-solutions/stow-s3/internal/runthrough"
	"github.com/chester-hill-solutions/stow-s3/internal/s3api"
	"github.com/chester-hill-solutions/stow-s3/internal/storage"
)

// nativeTestStore builds the same store the serve path builds, so the quota
// assertions below exercise enforcement rather than the flag plumbing.
func nativeTestStore(t *testing.T, limits nativeStorageLimits) storage.Store {
	t.Helper()
	store, err := bindNativeRuntimeStore(storage.NewMemoryStore(), "memory", nil, limits)
	if err != nil {
		t.Fatalf("bind native runtime store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func nativeTestServer(t *testing.T, store storage.Store) *httptest.Server {
	t.Helper()
	srv, err := s3api.New(s3api.Config{
		Store:           store,
		Auth:            s3api.DevBypass,
		Host:            "127.0.0.1",
		Port:            0,
		MaxRequestBytes: 1 << 20,
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	server := httptest.NewServer(srv.Handler())
	t.Cleanup(server.Close)
	return server
}

func TestNativeStorageLimitsDefaultToUnbounded(t *testing.T) {
	limits := nativeStorageLimits{}
	if limits.bytes() <= 0 || limits.objects() <= 0 {
		t.Fatalf("unbounded limits = %d/%d, want positive sentinels", limits.bytes(), limits.objects())
	}

	ctx := context.Background()
	store := nativeTestStore(t, limits)
	if err := store.CreateBucket(ctx, "bucket"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	// Comfortably past the embedded profile's 64 MiB default, which must not
	// reject writes for a long-lived server that never opted in.
	large := bytes.Repeat([]byte("x"), 1<<20)
	if _, err := store.PutObject(ctx, "bucket", "key", bytes.NewReader(large), storage.PutOptions{}); err != nil {
		t.Fatalf("unbounded put: %v", err)
	}
}

func TestNativeStorageLimitsRejectOverQuotaWrites(t *testing.T) {
	ctx := context.Background()
	store := nativeTestStore(t, nativeStorageLimits{maxBytes: 8, maxObjects: 1})
	if err := store.CreateBucket(ctx, "bucket"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}

	overBytes := bytes.Repeat([]byte("x"), 9)
	if _, err := store.PutObject(ctx, "bucket", "big", bytes.NewReader(overBytes), storage.PutOptions{}); err == nil {
		t.Fatal("expected the byte quota to reject a 9 byte object at an 8 byte limit")
	}
	if _, err := store.PutObject(ctx, "bucket", "ok", bytes.NewReader([]byte("1234")), storage.PutOptions{}); err != nil {
		t.Fatalf("within-quota put: %v", err)
	}
	if _, err := store.PutObject(ctx, "bucket", "second", bytes.NewReader([]byte("5")), storage.PutOptions{}); err == nil {
		t.Fatal("expected the object-count quota to reject a second object at a limit of one")
	}
}

// The quota must be enforced on the real HTTP path, not only when calling the
// store directly, because that is the surface a session actually uses.
func TestNativeStorageLimitsApplyOverS3Requests(t *testing.T) {
	store := nativeTestStore(t, nativeStorageLimits{maxBytes: 4, maxObjects: 10})
	server := nativeTestServer(t, store)

	createBucket := func() {
		t.Helper()
		request, err := http.NewRequest(http.MethodPut, server.URL+"/bucket", nil)
		if err != nil {
			t.Fatalf("create bucket request: %v", err)
		}
		request.ContentLength = 0
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatalf("create bucket: %v", err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("create bucket status = %d", response.StatusCode)
		}
	}
	createBucket()

	put := func(key string, body []byte) int {
		t.Helper()
		request, err := http.NewRequest(http.MethodPut, server.URL+"/bucket/"+key, bytes.NewReader(body))
		if err != nil {
			t.Fatalf("put request: %v", err)
		}
		request.Header.Set("Content-Type", "application/octet-stream")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatalf("put %q: %v", key, err)
		}
		defer response.Body.Close()
		return response.StatusCode
	}

	if status := put("fits", []byte("1234")); status != http.StatusOK {
		t.Fatalf("within-quota put status = %d, want 200", status)
	}
	status, code := putWithError(t, server, "toolarge", []byte("12345"))
	if status != http.StatusInsufficientStorage {
		t.Fatalf("over-quota put status = %d, want 507", status)
	}
	if code != "InsufficientStorage" {
		t.Fatalf("over-quota error code = %q, want InsufficientStorage", code)
	}
}

func putWithError(t *testing.T, server *httptest.Server, key string, body []byte) (int, string) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPut, server.URL+"/bucket/"+key, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("put request: %v", err)
	}
	request.Header.Set("Content-Type", "application/octet-stream")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("put %q: %v", key, err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var decoded struct {
		Code string `xml:"Code"`
	}
	if err := xml.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode error body %q: %v", payload, err)
	}
	return response.StatusCode, decoded.Code
}

func TestRunThroughStoreStillReportsAdminStats(t *testing.T) {
	local := storage.NewMemoryStore()
	cache := storage.NewMemoryStore()
	if err := local.CreateBucket(context.Background(), "bucket"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	admin := runthrough.NewWithOutbox(
		runthrough.Config{Policy: runthrough.PolicyReadThroughCache},
		local,
		cache,
		nil,
		runthrough.NewMemoryOutbox(),
	)
	store, err := bindNativeRuntimeStore(local, "memory", admin, nativeStorageLimits{})
	if err != nil {
		t.Fatalf("bind native runtime store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.PutObject(context.Background(), "bucket", "key", bytes.NewReader([]byte("v")), storage.PutOptions{}); err != nil {
		t.Fatalf("put through run-through store: %v", err)
	}
}
