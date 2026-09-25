package s3api

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEnforceContentLengthRejectsMissingHeader(t *testing.T) {
	req := httptest.NewRequest("PUT", "/bucket/key", nil)
	if err := enforceContentLength(req, nil); err == nil {
		t.Fatal("expected missing Content-Length")
	}
}

func TestEnforceContentLengthRejectsMismatch(t *testing.T) {
	req := httptest.NewRequest("PUT", "/bucket/key", strings.NewReader("body"))
	req.ContentLength = 99
	if err := enforceContentLength(req, []byte("body")); err == nil {
		t.Fatal("expected Content-Length mismatch")
	}
}

func TestEnforceContentLengthAcceptsAMatchingBody(t *testing.T) {
	req := httptest.NewRequest("PUT", "/bucket/key", strings.NewReader("body"))
	// The check reads the header, not the field, and httptest sets only the
	// field, so a real request shape has to be built here.
	req.Header.Set("Content-Length", "4")
	if err := enforceContentLength(req, []byte("body")); err != nil {
		t.Fatalf("matching Content-Length rejected: %v", err)
	}
}

// TestRequestBodyReadsTheSocketOnlyOnce is the property the read-once change
// exists for. Every consumer in this package needs the whole body, and each one
// used to make its own full-size copy, which is where the measured per-MiB
// memory amplification came from.
func TestRequestBodyReadsTheSocketOnlyOnce(t *testing.T) {
	req := withBodyCache(httptest.NewRequest("PUT", "/bucket/key", strings.NewReader("payload")))
	body, err := requestBody(req)
	if err != nil {
		t.Fatalf("first read: %v", err)
	}
	if string(body) != "payload" {
		t.Fatalf("body = %q, want %q", body, "payload")
	}

	// The body is restored, so a second read is served from the same bytes rather
	// than from the socket.
	again, err := requestBody(req)
	if err != nil {
		t.Fatalf("second read: %v", err)
	}
	if string(again) != "payload" {
		t.Fatalf("second read = %q, want %q", again, "payload")
	}
	if &again[0] != &body[0] {
		t.Error("a second read should reuse the same buffer rather than reallocating")
	}
}

// TestRequestBodyReusesTheBytesPreparedForAuth covers the path that matters for a
// real authenticated request: SigV4 needs the whole body, and the handler must
// not read it a second time.
func TestRequestBodyReusesTheBytesPreparedForAuth(t *testing.T) {
	req := withBodyCache(httptest.NewRequest("PUT", "/bucket/key", strings.NewReader("payload")))
	prepared, err := prepareRequestForAuth(req)
	if err != nil {
		t.Fatalf("prepare for auth: %v", err)
	}
	body, err := requestBody(prepared)
	if err != nil {
		t.Fatalf("request body: %v", err)
	}
	if string(body) != "payload" {
		t.Fatalf("body = %q, want %q", body, "payload")
	}
	// The bytes must also still be on the request, because the store reads from
	// r.Body rather than from the context.
	replayed, err := requestBody(prepared)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if string(replayed) != "payload" {
		t.Fatalf("replayed = %q, want %q", replayed, "payload")
	}
}

func TestRequestBodyIsEmptyForABodylessRequest(t *testing.T) {
	req := withBodyCache(httptest.NewRequest("POST", "/bucket", nil))
	body, err := requestBody(req)
	if err != nil {
		t.Fatalf("request body: %v", err)
	}
	if len(body) != 0 {
		t.Fatalf("body = %q, want empty", body)
	}
}
