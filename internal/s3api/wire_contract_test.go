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

// objectPath builds a request path for a key, percent-encoding each segment the
// way a client does. PathEscape escapes the separators too, so the segments are
// escaped individually and rejoined - which is also why the server has to decode
// the copy-source header rather than trust the key it was sent.
func objectPath(bucket, key string) string {
	segments := strings.Split(key, "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	return "/" + bucket + "/" + strings.Join(segments, "/")
}

func putOverWire(t *testing.T, ts *httptest.Server, bucket, key, body string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, ts.URL+objectPath(bucket, key), strings.NewReader(body))
	if err != nil {
		t.Fatalf("build put for %s/%s: %v", bucket, key, err)
	}
	req.ContentLength = int64(len(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("put %s/%s: %v", bucket, key, err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put %s/%s = %d, want 200", bucket, key, resp.StatusCode)
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

func headStatusOverWire(t *testing.T, ts *httptest.Server, bucket, key string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodHead, ts.URL+objectPath(bucket, key), nil)
	if err != nil {
		t.Fatalf("build head for %s/%s: %v", bucket, key, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("head %s/%s: %v", bucket, key, err)
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
func copyOverWire(t *testing.T, ts *httptest.Server, dstBucket, dstKey, encodedSource string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, ts.URL+objectPath(dstBucket, dstKey), nil)
	if err != nil {
		t.Fatalf("build copy to %s/%s: %v", dstBucket, dstKey, err)
	}
	req.Header.Set("x-amz-copy-source", encodedSource)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("copy to %s/%s: %v", dstBucket, dstKey, err)
	}
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)
	return resp.StatusCode
}

func getOverWire(t *testing.T, ts *httptest.Server, bucket, key string) (string, int) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, ts.URL+objectPath(bucket, key), nil)
	if err != nil {
		t.Fatalf("build get for %s/%s: %v", bucket, key, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get %s/%s: %v", bucket, key, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return string(body), resp.StatusCode
}

// The x-amz-copy-source header carries a percent-encoded key, and the server has
// to decode it.
//
// The compat contract says so - "URL-encoded key segments" - and parseCopySource
// split the header on the first slash and used both halves verbatim. Every SDK
// percent-encodes the key, so a key containing a space, a plus, a percent sign
// or a slash could not be copied at all: the server looked for a key literally
// spelled with the escapes in it and answered NoSuchKey for an object that
// existed. No test issued a CopyObject, so the operation had no coverage at all.
//
// Decoding has to happen after the bucket is split off, not before. A key whose
// slash is encoded as %2F would otherwise become a separator and the split would
// land in the wrong place, so a key with a real slash would work and a key
// containing an encoded one would not.
//
// The plus is the case that distinguishes the two unescape functions: in a path
// segment + is a literal plus, so a key spelled "a+b" must not come back as
// "a b". QueryUnescape would rewrite it.
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
			putOverWire(t, ts, "src", tc.key, want)

			if status := copyOverWire(t, ts, "dst", "copied.txt", "/src/"+tc.encoded); status != http.StatusOK {
				t.Fatalf("copy of %q (encoded %q) = %d, want 200", tc.key, tc.encoded, status)
			}
			got, status := getOverWire(t, ts, "dst", "copied.txt")
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
	putOverWire(t, ts, "src", "present.txt", "bytes")

	if status := copyOverWire(t, ts, "dst", "copied.txt", "/src/absent%20key.txt"); status != http.StatusNotFound {
		t.Fatalf("copy of a missing key = %d, want 404", status)
	}
}

// The <Deleted> entries in a multi-object delete response name the keys that
// were deleted.
//
// This is the assertion that would have caught the workspace backend's
// complement, and it belongs at the S3 surface rather than only in the store
// contract: the handler copies the store's return value straight into the
// response body, so a store that reports the survivors produces a response
// naming objects still on disk and omitting the ones that are gone. The
// store-level case proves the return value; this one proves the wire inherits
// it.
func TestDeleteObjectsResponseNamesTheKeysItDeleted(t *testing.T) {
	for name, newStore := range wireStores() {
		t.Run(name, func(t *testing.T) {
			ts := newStoreServer(t, newStore(t))
			createBucketOverWire(t, ts, "wirebucket")
			putOverWire(t, ts, "wirebucket", "gone/one", "value")
			putOverWire(t, ts, "wirebucket", "gone/two", "value")

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
				if status := headStatusOverWire(t, ts, "wirebucket", key); status != http.StatusNotFound {
					t.Fatalf("head %s after reported deletion = %d, want 404", key, status)
				}
			}
		})
	}
}
