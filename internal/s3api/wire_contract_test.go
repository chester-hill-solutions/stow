package s3api_test

// Wire-level assertions: what a client is told, rather than what a store
// returns.
//
// The handler is mostly a translator, and a translator is where a backend's
// return value becomes a promise. A store-level test proves the return value;
// these prove the wire inherits it, which is the part a caller actually sees.

import (
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/chester-hill-solutions/stow-s3/internal/s3api"
	"github.com/chester-hill-solutions/stow-s3/internal/storage"
	"github.com/chester-hill-solutions/stow-s3/internal/storage/fs"
	"github.com/chester-hill-solutions/stow-s3/internal/storage/workspace"
)

// newStoreServer starts an S3 server over the given store. The store-level
// contract suite proves what a backend returns; these helpers are how a test
// observes the same thing at the wire.
func newStoreServer(t *testing.T, store storage.Store) *httptest.Server {
	t.Helper()
	srv, err := s3api.New(s3api.Config{
		Store: store,
		Auth:  s3api.DevBypass,
		Host:  "127.0.0.1",
		Port:  0,
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

// objectRef names an object on the wire.
//
// The bucket/key pair appears in nearly every helper here. Carried as two
// parameters it put four of them over the parameter limit, which is the ratchet
// correctly reporting that the pair is one thing and should be said once.
type objectRef struct {
	bucket string
	key    string
}

// path builds a request path for the object, percent-encoding each segment the
// way a client does. PathEscape escapes the separators too, so the segments are
// escaped individually and rejoined - which is also why the server has to decode
// the copy-source header rather than trust the key it was sent.
func (o objectRef) path() string {
	segments := strings.Split(o.key, "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	return "/" + o.bucket + "/" + strings.Join(segments, "/")
}

func putOverWire(t *testing.T, ts *httptest.Server, ref objectRef, body string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, ts.URL+ref.path(), strings.NewReader(body))
	if err != nil {
		t.Fatalf("build put for %s: %v", ref, err)
	}
	req.ContentLength = int64(len(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("put %s: %v", ref, err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put %s = %d, want 200", ref, resp.StatusCode)
	}
}

func createBucketOverWire(t *testing.T, ts *httptest.Server, bucket string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, ts.URL+"/"+bucket, nil)
	if err != nil {
		t.Fatalf("build create bucket %s: %v", bucket, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("create bucket %s: %v", bucket, err)
	}
	resp.Body.Close()
}

// deleteOverWire issues a multi-object delete and returns the keys the response
// named as deleted, in the order it named them.
func deleteOverWire(t *testing.T, ts *httptest.Server, bucket string, keys ...string) []string {
	t.Helper()
	var request strings.Builder
	request.WriteString("<Delete>")
	for _, key := range keys {
		request.WriteString("<Object><Key>")
		request.WriteString(key)
		request.WriteString("</Key></Object>")
	}
	request.WriteString("</Delete>")
	body := request.String()

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/"+bucket+"?delete", strings.NewReader(body))
	req.ContentLength = int64(len(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("delete objects: %v", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete status = %d, want 200: %s", resp.StatusCode, data)
	}
	var result struct {
		Deleted []struct {
			Key string `xml:"Key"`
		} `xml:"Deleted"`
	}
	if err := xml.Unmarshal(data, &result); err != nil {
		t.Fatalf("parse response %q: %v", data, err)
	}
	reported := make([]string, 0, len(result.Deleted))
	for _, entry := range result.Deleted {
		reported = append(reported, entry.Key)
	}
	return reported
}

func headStatusOverWire(t *testing.T, ts *httptest.Server, ref objectRef) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodHead, ts.URL+ref.path(), nil)
	if err != nil {
		t.Fatalf("build head for %s: %v", ref, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("head %s: %v", ref, err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// wireStores is every store the S3 surface can be served from. A wire-level
// assertion that runs against all of them is what stops one backend's return
// value from being reinterpreted by the handler.
func wireStores() map[string]func(t *testing.T) storage.Store {
	return map[string]func(t *testing.T) storage.Store{
		"memory": func(t *testing.T) storage.Store { return storage.NewMemoryStore() },
		"filesystem": func(t *testing.T) storage.Store {
			store, err := fs.NewFilesystemStore(t.TempDir())
			if err != nil {
				t.Fatalf("new filesystem store: %v", err)
			}
			return store
		},
		"workspace": func(t *testing.T) storage.Store {
			store, err := workspace.New(workspace.Options{Root: t.TempDir(), Bucket: "wire"})
			if err != nil {
				t.Fatalf("new workspace store: %v", err)
			}
			return store
		},
	}
}

// copyOverWire issues a CopyObject naming src as an already-encoded copy-source
// header value, exactly as an SDK builds it, and returns the status code.
func copyOverWire(t *testing.T, ts *httptest.Server, dst objectRef, encodedSource string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, ts.URL+dst.path(), nil)
	if err != nil {
		t.Fatalf("build copy to %s: %v", dst, err)
	}
	req.Header.Set("x-amz-copy-source", encodedSource)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("copy to %s: %v", dst, err)
	}
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)
	return resp.StatusCode
}

func getOverWire(t *testing.T, ts *httptest.Server, ref objectRef) (string, int) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, ts.URL+ref.path(), nil)
	if err != nil {
		t.Fatalf("build get for %s: %v", ref, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get %s: %v", ref, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return string(body), resp.StatusCode
}

// The x-amz-copy-source header carries a percent-encoded key and the server
// decodes it, so a key containing a space, a plus, a percent sign or a slash is
// copyable. Every SDK encodes the key, so an undecoded header means the server
// looks for a key spelled with the escapes still in it.
//
// Two entries carry the constraints. "encoded slash" fails if the server decodes
// before splitting the bucket off, because %2F would then become the separator -
// and that mistake passes "real slash", so the pair is what detects it. "literal
// plus" fails if the server uses QueryUnescape, because in a path segment a plus
// is not an encoded space.
func TestCopyObjectDecodesTheCopySourceHeader(t *testing.T) {
	cases := []struct {
		name    string
		key     string
		encoded string
	}{
		{"space", "with space.txt", "with%20space.txt"},
		{"literal plus", "a+b.txt", "a%2Bb.txt"},
		{"percent", "100%.txt", "100%25.txt"},
		{"encoded slash", "dir/inner.txt", "dir%2Finner.txt"},
		{"real slash", "dir/plain.txt", "dir/plain.txt"},
		{"encoded slash and space", "dir/a b+c%.txt", "dir%2Fa%20b%2Bc%25.txt"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := newStoreServer(t, storage.NewMemoryStore())
			createBucketOverWire(t, ts, "src")
			createBucketOverWire(t, ts, "dst")
			const want = "the original bytes"
			putOverWire(t, ts, objectRef{"src", tc.key}, want)

			if status := copyOverWire(t, ts, objectRef{"dst", "copied.txt"}, "/src/"+tc.encoded); status != http.StatusOK {
				t.Fatalf("copy of %q (encoded %q) = %d, want 200", tc.key, tc.encoded, status)
			}
			got, status := getOverWire(t, ts, objectRef{"dst", "copied.txt"})
			if status != http.StatusOK {
				t.Fatalf("get copy = %d, want 200", status)
			}
			if got != want {
				t.Fatalf("copied body = %q, want %q", got, want)
			}
		})
	}
}

// A key that does not exist is still NoSuchKey after decoding, so the fix cannot
// be "decode and accept anything".
func TestCopyObjectRejectsAnUnresolvableSource(t *testing.T) {
	ts := newStoreServer(t, storage.NewMemoryStore())
	createBucketOverWire(t, ts, "src")
	createBucketOverWire(t, ts, "dst")
	putOverWire(t, ts, objectRef{"src", "present.txt"}, "bytes")

	if status := copyOverWire(t, ts, objectRef{"dst", "copied.txt"}, "/src/absent%20key.txt"); status != http.StatusNotFound {
		t.Fatalf("copy of a missing key = %d, want 404", status)
	}
}

// rangeOverWire issues a GET with a Range header and returns the status, the
// body, and the two headers a client uses to reconstruct what it received.
func rangeOverWire(t *testing.T, ts *httptest.Server, ref objectRef, spec string) (int, string, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, ts.URL+ref.path(), nil)
	if err != nil {
		t.Fatalf("build ranged get for %s: %v", ref, err)
	}
	req.Header.Set("Range", spec)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("ranged get %s: %v", spec, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body), resp.Header.Get("Content-Range")
}

// A range whose end runs past the last byte is clamped, not refused. RFC 9110
// requires a recipient to treat an unsatisfiable end as the last byte, and a GET
// for bytes=0-99 of a 50-byte object answers 206 with all 50 bytes. Clients ask
// for "the rest of this" by naming an offset they inferred rather than measured,
// so this is reachable without doing anything unusual.
//
// The clamping cases and the refusing cases are both here because the two
// directions are one decision: S3 refuses a range that begins past the object and
// clamps one that merely ends too far. Clamping the start as well would turn
// every 416 below into a silently empty 206.
func TestGetObjectRangeClampsAnEndPastTheLastByte(t *testing.T) {
	const body = "01234567890123456789012345678901234567890123456789" // 50 bytes
	if len(body) != 50 {
		t.Fatalf("fixture is %d bytes, want 50", len(body))
	}

	cases := []struct {
		name         string
		spec         string
		wantStatus   int
		wantBody     string
		wantRangeHdr string
	}{
		{"end far past the object", "bytes=0-999", http.StatusPartialContent, body, "bytes 0-49/50"},
		{"end just past the object", "bytes=0-50", http.StatusPartialContent, body, "bytes 0-49/50"},
		{"tail with an over-long end", "bytes=40-999", http.StatusPartialContent, body[40:], "bytes 40-49/50"},
		{"exact whole object", "bytes=0-49", http.StatusPartialContent, body, "bytes 0-49/50"},
		{"open ended", "bytes=0-", http.StatusPartialContent, body, "bytes 0-49/50"},
		{"open ended mid object", "bytes=10-", http.StatusPartialContent, body[10:], "bytes 10-49/50"},
		{"suffix shorter than the object", "bytes=-10", http.StatusPartialContent, body[40:], "bytes 40-49/50"},
		{"suffix longer than the object", "bytes=-999", http.StatusPartialContent, body, "bytes 0-49/50"},
		{"interior range", "bytes=10-19", http.StatusPartialContent, body[10:20], "bytes 10-19/50"},

		// Still refusals. A range that begins past the last byte has no
		// satisfiable interpretation, and a zero-length suffix is not a range.
		{"start at the size", "bytes=50-60", http.StatusRequestedRangeNotSatisfiable, "", "bytes */50"},
		{"start past the size", "bytes=60-70", http.StatusRequestedRangeNotSatisfiable, "", "bytes */50"},
		{"start past with an over-long end", "bytes=60-999", http.StatusRequestedRangeNotSatisfiable, "", "bytes */50"},
		{"end before start", "bytes=10-5", http.StatusRequestedRangeNotSatisfiable, "", "bytes */50"},
		{"zero length suffix", "bytes=-0", http.StatusRequestedRangeNotSatisfiable, "", "bytes */50"},
		{"not a number", "bytes=abc", http.StatusRequestedRangeNotSatisfiable, "", "bytes */50"},
		{"not a byte range", "items=0-10", http.StatusRequestedRangeNotSatisfiable, "", "bytes */50"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := newStoreServer(t, storage.NewMemoryStore())
			createBucketOverWire(t, ts, "ranged")
			putOverWire(t, ts, objectRef{"ranged", "object.bin"}, body)

			status, got, rangeHdr := rangeOverWire(t, ts, objectRef{"ranged", "object.bin"}, tc.spec)
			if status != tc.wantStatus {
				t.Fatalf("%s = %d, want %d (Content-Range %q)", tc.spec, status, tc.wantStatus, rangeHdr)
			}
			if tc.wantStatus == http.StatusPartialContent {
				if got != tc.wantBody {
					t.Fatalf("%s body = %q, want %q", tc.spec, got, tc.wantBody)
				}
			} else if !strings.Contains(got, "<Code>InvalidRange</Code>") {
				// A refusal has to be the S3 error, not an empty body or a
				// truncated object.
				t.Fatalf("%s refusal body = %q, want an InvalidRange error", tc.spec, got)
			}
			if rangeHdr != tc.wantRangeHdr {
				t.Fatalf("%s Content-Range = %q, want %q", tc.spec, rangeHdr, tc.wantRangeHdr)
			}
		})
	}
}

// A ranged GET that asks for the whole object still answers 206, not 200. S3
// does, and a client that sent a Range header is entitled to the partial-content
// status and the Content-Range that goes with it.
func TestGetObjectRangeOverTheWholeObjectIsPartialContent(t *testing.T) {
	ts := newStoreServer(t, storage.NewMemoryStore())
	createBucketOverWire(t, ts, "ranged")
	putOverWire(t, ts, objectRef{"ranged", "object.bin"}, "short")

	status, body, rangeHdr := rangeOverWire(t, ts, objectRef{"ranged", "object.bin"}, "bytes=0-999")
	if status != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", status)
	}
	if body != "short" {
		t.Fatalf("body = %q, want %q", body, "short")
	}
	if rangeHdr != "bytes 0-4/5" {
		t.Fatalf("Content-Range = %q, want %q", rangeHdr, "bytes 0-4/5")
	}
}

// The <Deleted> entries in a multi-object delete response name the keys that
// were deleted. The handler copies the store's return value into the response
// body, so this runs against all three stores: the store contract case proves
// what a backend returns, and only this proves the wire inherits it.
func TestDeleteObjectsResponseNamesTheKeysItDeleted(t *testing.T) {
	for name, newStore := range wireStores() {
		t.Run(name, func(t *testing.T) {
			ts := newStoreServer(t, newStore(t))
			createBucketOverWire(t, ts, "wirebucket")
			putOverWire(t, ts, objectRef{"wirebucket", "gone/one"}, "value")
			putOverWire(t, ts, objectRef{"wirebucket", "gone/two"}, "value")

			// The absent key is the tell. It was never deleted, so it must not
			// appear; a complement implementation reports exactly this one.
			reported := deleteOverWire(t, ts, "wirebucket", "gone/one", "never-existed", "gone/two")
			want := []string{"gone/one", "gone/two"}
			if !slices.Equal(reported, want) {
				t.Fatalf("reported deleted = %v, want %v; the absent key must not appear and the two real deletions must", reported, want)
			}

			// And the named keys are genuinely gone, so the response is not
			// merely plausible.
			for _, key := range want {
				if status := headStatusOverWire(t, ts, objectRef{"wirebucket", key}); status != http.StatusNotFound {
					t.Fatalf("head %s after reported deletion = %d, want 404", key, status)
				}
			}
		})
	}
}
