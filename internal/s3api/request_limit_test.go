package s3api_test

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/chester-hill-solutions/stow-s3/internal/s3api"
	"github.com/chester-hill-solutions/stow-s3/internal/storage"
)

func newLimitedServer(t *testing.T, limit int64) *httptest.Server {
	t.Helper()
	srv, err := s3api.New(s3api.Config{
		Store:           storage.NewMemoryStore(),
		Auth:            s3api.DevBypass,
		Host:            "127.0.0.1",
		Port:            0,
		MaxRequestBytes: limit,
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	server := httptest.NewServer(srv.Handler())
	t.Cleanup(server.Close)
	createBucketAt(t, server, "limited")
	return server
}

func createBucketAt(t *testing.T, server *httptest.Server, bucket string) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPut, server.URL+"/"+bucket, nil)
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

func putObject(t *testing.T, server *httptest.Server, key string, body io.Reader) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodPut, server.URL+"/limited/"+key, body)
	if err != nil {
		t.Fatalf("put request: %v", err)
	}
	request.Header.Set("Content-Type", "application/octet-stream")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("put %q: %v", key, err)
	}
	return response
}

func assertEntityTooLarge(t *testing.T, response *http.Response) {
	t.Helper()
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read error body: %v", err)
	}
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", response.StatusCode, body)
	}
	var decoded struct {
		Code string `xml:"Code"`
	}
	if err := xml.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode error body %q: %v", body, err)
	}
	if decoded.Code != "EntityTooLarge" {
		t.Fatalf("error code = %q, want EntityTooLarge", decoded.Code)
	}
}

func TestPutObjectRejectsOversizedBody(t *testing.T) {
	const limit = 1024
	server := newLimitedServer(t, limit)

	response := putObject(t, server, "big", bytes.NewReader(bytes.Repeat([]byte("a"), limit+1)))
	assertEntityTooLarge(t, response)
}

func TestBodyAtLimitIsAccepted(t *testing.T) {
	const limit = 1024
	server := newLimitedServer(t, limit)

	exact := bytes.Repeat([]byte("a"), limit)
	response := putObject(t, server, "exact", bytes.NewReader(exact))
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("status = %d, want 200; body=%s", response.StatusCode, body)
	}
}

// A body sent with chunked transfer encoding declares no length at all, so the
// cap has to be enforced on the bytes actually read.
func TestChunkedOversizedBodyIsBounded(t *testing.T) {
	const limit = 1024
	server := newLimitedServer(t, limit)

	request, err := http.NewRequest(http.MethodPut, server.URL+"/limited/chunked", &endlessReader{})
	if err != nil {
		t.Fatalf("put request: %v", err)
	}
	request.Header.Set("Content-Type", "application/octet-stream")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("put chunked: %v", err)
	}
	assertEntityTooLarge(t, response)
}

// endlessReader streams far more than any declared length, so the server must
// stop reading once the limit is reached.
type endlessReader struct {
	bytesRead int
}

func (r *endlessReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'a'
	}
	r.bytesRead += len(p)
	return len(p), nil
}

func TestMultipartPartRejectsOversizedBody(t *testing.T) {
	const limit = 1024
	server := newLimitedServer(t, limit)

	uploadID := initiateMultipart(t, server, "multipart")
	part := bytes.Repeat([]byte("b"), limit+1)
	request, err := http.NewRequest(
		http.MethodPut,
		fmt.Sprintf("%s/limited/multipart?partNumber=1&uploadId=%s", server.URL, uploadID),
		bytes.NewReader(part),
	)
	if err != nil {
		t.Fatalf("upload part request: %v", err)
	}
	request.Header.Set("Content-Type", "application/octet-stream")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("upload part: %v", err)
	}
	assertEntityTooLarge(t, response)
}

func initiateMultipart(t *testing.T, server *httptest.Server, key string) string {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, server.URL+"/limited/"+key+"?uploads", nil)
	if err != nil {
		t.Fatalf("initiate request: %v", err)
	}
	request.ContentLength = 0
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("initiate multipart: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("initiate status = %d; body=%s", response.StatusCode, body)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read initiate body: %v", err)
	}
	var initiated struct {
		UploadID string `xml:"UploadId"`
	}
	if err := xml.Unmarshal(body, &initiated); err != nil {
		t.Fatalf("decode initiate response %q: %v", body, err)
	}
	if initiated.UploadID == "" {
		t.Fatal("initiate multipart did not return an upload id")
	}
	return initiated.UploadID
}

func TestDefaultRequestLimitAppliesWhenUnset(t *testing.T) {
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
	t.Cleanup(server.Close)
	createBucketAt(t, server, "limited")

	oversized := bytes.Repeat([]byte("c"), int(s3api.DefaultMaxRequestBytes)+1)
	request, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodPut,
		server.URL+"/limited/default",
		bytes.NewReader(oversized),
	)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	request.Header.Set("Content-Type", "application/octet-stream")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	assertEntityTooLarge(t, response)
}
