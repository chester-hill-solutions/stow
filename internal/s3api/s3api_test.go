package s3api_test

import (
	"bytes"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chester-hill-solutions/stow/internal/s3api"
	"github.com/chester-hill-solutions/stow/internal/storage"
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
