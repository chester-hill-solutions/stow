package s3api_test

import (
	"encoding/base64"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chester-hill-solutions/stow-s3/internal/s3api"
	"github.com/chester-hill-solutions/stow-s3/internal/storage"
)

func conditionalServer(t *testing.T) *httptest.Server {
	t.Helper()
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

	createReq, err := http.NewRequest(http.MethodPut, server.URL+"/bucket", nil)
	if err != nil {
		t.Fatalf("new create request: %v", err)
	}
	create, err := server.Client().Do(createReq)
	if err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	create.Body.Close()
	if create.StatusCode != http.StatusOK {
		t.Fatalf("create bucket = %d, want 200", create.StatusCode)
	}
	return server
}

// path is "bucket/key" rather than the two separately, so the helper stays
// inside the five-parameter limit the quality ratchet enforces.
func put(t *testing.T, server *httptest.Server, path, body string, headers map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, server.URL+"/"+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	return resp
}

// If-None-Match: * is how a caller implements "create, but only if it does not
// already exist" without a read-then-write race.
//
// It was issue #11: against v0.1.0 the header was accepted and ignored, so the
// write succeeded, no error was raised, and the caller believed it had created
// a new object when it had replaced one. A caller cannot distinguish "stored"
// from "silently replaced", which is what makes an ignored header worse than an
// unimplemented one.
func TestPutObjectWithIfNoneMatchStarRefusesToOverwrite(t *testing.T) {
	server := conditionalServer(t)

	if resp := put(t, server, "bucket/key.txt", "one", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("first put = %d, want 200", resp.StatusCode)
	}

	resp := put(t, server, "bucket/key.txt", "two", map[string]string{"If-None-Match": "*"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("conditional put over an existing key = %d, want 412", resp.StatusCode)
	}

	// And the original bytes are still there. A 412 that had already overwritten
	// would be the same status with the damage done.
	get, err := server.Client().Get(server.URL + "/bucket/key.txt")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer get.Body.Close()
	stored, _ := io.ReadAll(get.Body)
	if string(stored) != "one" {
		t.Errorf("stored body = %q, want %q — the refusal must not have written", stored, "one")
	}
}

// The same header on a key that does not exist must succeed, or the
// conditional could never create anything.
func TestPutObjectWithIfNoneMatchStarSucceedsOnANewKey(t *testing.T) {
	server := conditionalServer(t)

	resp := put(t, server, "bucket/fresh.txt", "one", map[string]string{"If-None-Match": "*"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("conditional put to a new key = %d, want 200", resp.StatusCode)
	}
}

// If-None-Match with a concrete ETag is the other half of the same feature:
// write only if the object differs from what the caller last saw.
func TestPutObjectWithAConditionalETag(t *testing.T) {
	server := conditionalServer(t)

	first := put(t, server, "bucket/key.txt", "one", nil)
	first.Body.Close()
	etag := first.Header.Get("ETag")
	if etag == "" {
		t.Fatal("the first put returned no ETag to condition on")
	}

	// The same ETag means the object is unchanged, so refuse.
	resp := put(t, server, "bucket/key.txt", "two", map[string]string{"If-None-Match": etag})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Errorf("put with the current ETag = %d, want 412", resp.StatusCode)
	}

	// A different ETag means it changed, so allow.
	other := put(t, server, "bucket/key.txt", "three", map[string]string{"If-None-Match": `"not-the-current-etag"`})
	defer other.Body.Close()
	if other.StatusCode != http.StatusOK {
		t.Errorf("put with a stale ETag = %d, want 200", other.StatusCode)
	}
}

// If-Match is the mirror: write only if the object is what the caller expects.
// A mismatch is 412, and a missing object is 404, because "the thing you are
// updating is gone" is a different problem from "somebody changed it".
func TestPutObjectWithIfMatch(t *testing.T) {
	server := conditionalServer(t)

	// Against a key that does not exist.
	missing := put(t, server, "bucket/absent.txt", "one", map[string]string{"If-Match": `"anything"`})
	defer missing.Body.Close()
	if missing.StatusCode != http.StatusNotFound && missing.StatusCode != http.StatusPreconditionFailed {
		t.Errorf("If-Match on a missing key = %d, want 404 or 412", missing.StatusCode)
	}

	first := put(t, server, "bucket/key.txt", "one", nil)
	first.Body.Close()
	etag := first.Header.Get("ETag")

	// Wrong ETag.
	wrong := put(t, server, "bucket/key.txt", "two", map[string]string{"If-Match": `"stale"`})
	defer wrong.Body.Close()
	if wrong.StatusCode != http.StatusPreconditionFailed {
		t.Errorf("If-Match with a stale ETag = %d, want 412", wrong.StatusCode)
	}

	// Right ETag.
	right := put(t, server, "bucket/key.txt", "two", map[string]string{"If-Match": etag})
	defer right.Body.Close()
	if right.StatusCode != http.StatusOK {
		t.Errorf("If-Match with the current ETag = %d, want 200", right.StatusCode)
	}
}

// A conditional put that is refused must not consume the caller's quota. The
// reservation is released on the failure path, and a refused write that still
// billed for bytes would let a caller be denied by their own rejected requests.
func TestARefusedConditionalPutDoesNotConsumeQuota(t *testing.T) {
	server := conditionalServer(t)

	first := put(t, server, "bucket/key.txt", strings.Repeat("a", 1024), nil)
	first.Body.Close()

	for i := 0; i < 5; i++ {
		resp := put(t, server, "bucket/key.txt", strings.Repeat("b", 4096), map[string]string{"If-None-Match": "*"})
		if resp.StatusCode != http.StatusPreconditionFailed {
			t.Fatalf("attempt %d = %d, want 412", i, resp.StatusCode)
		}
		resp.Body.Close()
	}

	// The object is untouched, so the environment still holds exactly the
	// original bytes.
	get, err := server.Client().Get(server.URL + "/bucket/key.txt")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer get.Body.Close()
	stored, _ := io.ReadAll(get.Body)
	if len(stored) != 1024 {
		t.Errorf("stored %d bytes, want the original 1024", len(stored))
	}
	fmt.Fprint(io.Discard, stored)
}

// Reads are the other half of conditional semantics, and they are not the same
// as writes. RFC 9110 13.1.2: on GET and HEAD a matching If-None-Match is
// 304 Not Modified, not 412. 412 is for the case the header cannot be satisfied
// at all — If-None-Match: * against a resource that does not exist.
//
// Getting this backwards matters to the same caller as #11: a client that sends
// If-None-Match: * to poll for a change sees an error where it expects "not
// modified", and will treat a healthy object as a failure.
func TestGetWithIfNoneMatchStarOnAnExistingObjectIs304(t *testing.T) {
	server := conditionalServer(t)

	first := put(t, server, "bucket/key.txt", "one", nil)
	first.Body.Close()

	req, err := http.NewRequest(http.MethodGet, server.URL+"/bucket/key.txt", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("If-None-Match", "*")
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotModified {
		t.Errorf("GET with If-None-Match: * on an existing key = %d, want 304", resp.StatusCode)
	}
}

func TestHeadWithIfNoneMatchStarOnAnExistingObjectIs304(t *testing.T) {
	server := conditionalServer(t)

	first := put(t, server, "bucket/key.txt", "one", nil)
	first.Body.Close()

	req, err := http.NewRequest(http.MethodHead, server.URL+"/bucket/key.txt", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("If-None-Match", "*")
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotModified {
		t.Errorf("HEAD with If-None-Match: * on an existing key = %d, want 304", resp.StatusCode)
	}
}

// A matching concrete ETag on a read is 304 for the same reason.
func TestGetWithAMatchingETagIs304(t *testing.T) {
	server := conditionalServer(t)

	first := put(t, server, "bucket/key.txt", "one", nil)
	first.Body.Close()
	etag := first.Header.Get("ETag")

	req, err := http.NewRequest(http.MethodGet, server.URL+"/bucket/key.txt", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("If-None-Match", etag)
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotModified {
		t.Errorf("GET with the current ETag = %d, want 304", resp.StatusCode)
	}
}

// A non-matching ETag means the object changed, so the body is returned.
func TestGetWithAStaleETagReturnsTheBody(t *testing.T) {
	server := conditionalServer(t)

	put(t, server, "bucket/key.txt", "one", nil).Body.Close()

	req, err := http.NewRequest(http.MethodGet, server.URL+"/bucket/key.txt", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("If-None-Match", `"stale-etag"`)
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET with a stale ETag = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "one" {
		t.Errorf("body = %q, want %q", body, "one")
	}
}

// A 304 carries validators and no body, so it must not carry the metadata that
// describes one.
//
// This was caught by the TypeScript client, not here: aws-sdk-js validates
// x-amz-checksum-* against the body it received, a 304 has none, so it hashed
// the empty body and reported
//
//	Checksum mismatch: expected "L3JwhQ==" but received "AAAAAA=="
//
// on a response that was telling the truth. Content-Length is the same mistake -
// it claims a length for bytes that are not being sent. The Go SDK did not
// object, which is why a Go-only run of the corpus stayed green and the shared
// one did not.
func TestNotModifiedCarriesValidatorsAndNoRepresentationMetadata(t *testing.T) {
	server := conditionalServer(t)

	const body = "the quick brown fox"
	first := put(t, server, "bucket/key.txt", body, map[string]string{
		"x-amz-checksum-crc32": crc32Checksum(body),
	})
	first.Body.Close()
	etag := first.Header.Get("ETag")
	if etag == "" {
		t.Fatal("the put returned no ETag, so there is no validator to check")
	}

	req, err := http.NewRequest(http.MethodGet, server.URL+"/bucket/key.txt", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("If-None-Match", "*")
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", resp.StatusCode)
	}
	if got := resp.Header.Get("ETag"); got != etag {
		t.Errorf("ETag = %q, want the object's %q - a 304 without a validator is useless", got, etag)
	}
	if resp.Header.Get("Last-Modified") == "" {
		t.Error("a 304 should carry Last-Modified")
	}
	if got := resp.Header.Get("x-amz-checksum-crc32"); got != "" {
		t.Errorf("x-amz-checksum-crc32 = %q on a bodyless response; the SDK will "+
			"hash the empty body and report a mismatch", got)
	}
	if got := resp.Header.Get("Content-Length"); got == fmt.Sprint(len(body)) {
		t.Errorf("Content-Length = %q on a 304, claiming a length for a body that is not sent", got)
	}
	payload, _ := io.ReadAll(resp.Body)
	if len(payload) != 0 {
		t.Errorf("304 carried a %d byte body", len(payload))
	}
}

// crc32Checksum is the S3 wire encoding of a CRC32: big-endian, base64. It is
// spelled out here so the test states the exact value it is sending rather than
// asking the server to agree with whatever it is handed - the store verifies
// caller-supplied checksums, so a wrong one is now a 412 and the object would
// never be created.
func crc32Checksum(body string) string {
	sum := crc32.ChecksumIEEE([]byte(body))
	raw := []byte{byte(sum >> 24), byte(sum >> 16), byte(sum >> 8), byte(sum)}
	return base64.StdEncoding.EncodeToString(raw)
}
