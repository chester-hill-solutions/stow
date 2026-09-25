package s3api_test

import (
	"bytes"
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chester-hill-solutions/stow-s3/internal/runthrough"
	"github.com/chester-hill-solutions/stow-s3/internal/s3api"
	"github.com/chester-hill-solutions/stow-s3/internal/storage"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	store := storage.NewMemoryStore()
	srv, err := s3api.New(s3api.Config{
		Store: store,
		Auth:  s3api.DevBypass,
		Host:  "127.0.0.1",
		Port:  0,
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	return httptest.NewServer(srv.Handler())
}

func TestNewRequiresAuthentication(t *testing.T) {
	_, err := s3api.New(s3api.Config{
		Store: storage.NewMemoryStore(),
		Host:  "127.0.0.1",
		Port:  0,
	})
	if err == nil {
		t.Fatal("expected server creation to reject missing authentication")
	}
}

func TestServerAddrAndShutdownAreRaceFree(t *testing.T) {
	store := storage.NewMemoryStore()
	srv, err := s3api.New(s3api.Config{
		Store: store,
		Auth:  s3api.DevBypass,
		Host:  "127.0.0.1",
		Port:  0,
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ListenAndServe()
	}()

	var readers sync.WaitGroup
	stopReaders := make(chan struct{})
	readers.Add(1)
	go func() {
		defer readers.Done()
		for {
			select {
			case <-stopReaders:
				return
			default:
				_ = srv.Addr()
			}
		}
	}()

	deadline := time.Now().Add(2 * time.Second)
	for srv.Addr() == "" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if srv.Addr() == "" {
		close(stopReaders)
		readers.Wait()
		t.Fatal("server failed to bind")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	close(stopReaders)
	readers.Wait()

	select {
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not stop")
	}
}

func TestShutdownBeforeListenPreventsServe(t *testing.T) {
	srv, err := s3api.New(s3api.Config{
		Store: storage.NewMemoryStore(),
		Auth:  s3api.DevBypass,
		Host:  "127.0.0.1",
		Port:  0,
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown before listen: %v", err)
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	select {
	case err := <-errCh:
		if err != http.ErrServerClosed {
			t.Fatalf("listen error = %v, want http.ErrServerClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("server started after shutdown")
	}
}

func TestPutGetRoundtrip(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/mybucket", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create bucket status = %d", resp.StatusCode)
	}

	body := []byte("hello world")
	req, _ = http.NewRequest(http.MethodPut, ts.URL+"/mybucket/hello.txt", bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	req.Header.Set("Content-Type", "text/plain")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put status = %d", resp.StatusCode)
	}
	var putResult struct {
		ETag string `xml:"ETag"`
	}
	_ = xml.NewDecoder(resp.Body).Decode(&putResult)
	resp.Body.Close()
	if putResult.ETag == "" {
		t.Fatal("expected ETag in put response")
	}

	req, _ = http.NewRequest(http.MethodGet, ts.URL+"/mybucket/hello.txt", nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get status = %d", resp.StatusCode)
	}
	got, _ := io.ReadAll(resp.Body)
	if string(got) != "hello world" {
		t.Fatalf("body = %q", string(got))
	}
}

func TestUnsupportedMarkersDoNotFallThrough(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/mybucket?versioning", strings.NewReader(""))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("versioning request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("versioning status = %d, want 501", resp.StatusCode)
	}

	req, _ = http.NewRequest(http.MethodPut, ts.URL+"/mybucket/object", strings.NewReader("body"))
	req.Header.Set("X-Amz-Server-Side-Encryption", "aws:kms")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("encryption request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("encryption status = %d, want 400", resp.StatusCode)
	}
}

func TestHeadBucket(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/headtest", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	resp.Body.Close()

	req, _ = http.NewRequest(http.MethodHead, ts.URL+"/headtest", nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("head status = %d", resp.StatusCode)
	}

	req, _ = http.NewRequest(http.MethodHead, ts.URL+"/missing", nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("head missing: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("head missing status = %d", resp.StatusCode)
	}
}

func TestListObjectsV2(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	for _, step := range []struct {
		method string
		url    string
		body   string
	}{
		{http.MethodPut, ts.URL + "/listbucket", ""},
		{http.MethodPut, ts.URL + "/listbucket/a/1", "one"},
		{http.MethodPut, ts.URL + "/listbucket/a/2", "two"},
		{http.MethodPut, ts.URL + "/listbucket/b/1", "three"},
	} {
		var body io.Reader
		var cl int64
		if step.body != "" {
			body = strings.NewReader(step.body)
			cl = int64(len(step.body))
		}
		req, _ := http.NewRequest(step.method, step.url, body)
		if cl > 0 {
			req.ContentLength = cl
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("setup %s: %v", step.url, err)
		}
		resp.Body.Close()
	}

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/listbucket?list-type=2&prefix=a/", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list status = %d", resp.StatusCode)
	}
	data, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(data), "a/1") || !strings.Contains(string(data), "a/2") {
		t.Fatalf("unexpected list: %s", string(data))
	}
	if strings.Contains(string(data), "b/1") {
		t.Fatalf("prefix filter leaked b/1: %s", string(data))
	}
}

func TestDeleteObject(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	body := []byte("bye")
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/delbucket", nil)
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()

	req, _ = http.NewRequest(http.MethodPut, ts.URL+"/delbucket/key.txt", bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()

	req, _ = http.NewRequest(http.MethodDelete, ts.URL+"/delbucket/key.txt", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status = %d", resp.StatusCode)
	}

	// Idempotent delete
	req, _ = http.NewRequest(http.MethodDelete, ts.URL+"/delbucket/key.txt", nil)
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("idempotent delete status = %d", resp.StatusCode)
	}
}

func TestDeleteObjectsQuietSuppressesDeletedEntries(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/quietbucket", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	resp.Body.Close()

	for _, path := range []string{"/quietbucket/one", "/quietbucket/two"} {
		req, _ := http.NewRequest(http.MethodPut, ts.URL+path, strings.NewReader("value"))
		req.ContentLength = int64(len("value"))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("put %s: %v", path, err)
		}
		resp.Body.Close()
	}

	body := `<Delete><Quiet>true</Quiet><Object><Key>one</Key></Object><Object><Key>two</Key></Object></Delete>`
	req, _ = http.NewRequest(http.MethodPost, ts.URL+"/quietbucket?delete", strings.NewReader(body))
	req.ContentLength = int64(len(body))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("delete objects: %v", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete status = %d, want 200", resp.StatusCode)
	}
	if strings.Contains(string(data), "<Deleted>") {
		t.Fatalf("quiet response contained deleted entries: %s", data)
	}
}

func TestAdminOutboxDiscard(t *testing.T) {
	local := storage.NewMemoryStore()
	outbox := runthrough.NewMemoryOutbox()
	adapter := runthrough.NewWithOutbox(runthrough.Config{}, local, local, nil, outbox)
	entry, err := outbox.Enqueue(runthrough.OutboxEntry{
		Operation: runthrough.OutboxPut,
		Bucket:    "bucket",
		Key:       "key",
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	srv, err := s3api.New(s3api.Config{
		Store: adapter,
		Auth:  s3api.DevBypass,
		Host:  "127.0.0.1",
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/_stow/outbox/discard?id="+entry.ID, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("discard: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("discard status = %d, want 200", resp.StatusCode)
	}
	if pending := outbox.Pending(); len(pending) != 0 {
		t.Fatalf("pending entries = %+v, want none", pending)
	}
}

func TestAdminMetrics(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/_stow/metrics")
	if err != nil {
		t.Fatalf("metrics: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("metrics status = %d, want 200", resp.StatusCode)
	}
	if contentType := resp.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "text/plain") {
		t.Fatalf("metrics content type = %q, want text/plain", contentType)
	}
	data, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(data), "stow_cache_hits_total") {
		t.Fatalf("metrics body = %q, missing cache counter", data)
	}
}

func TestAdminHealth(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/_stow/health")
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d", resp.StatusCode)
	}
	data, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(data), `"status":"ok"`) && !strings.Contains(string(data), `"status": "ok"`) {
		t.Fatalf("unexpected health body: %s", string(data))
	}
}
