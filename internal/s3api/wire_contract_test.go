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

func putOverWire(t *testing.T, ts *httptest.Server, bucket, key, body string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/"+bucket+"/"+key, strings.NewReader(body))
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
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/"+bucket, nil)
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
	req, _ := http.NewRequest(http.MethodHead, ts.URL+"/"+bucket+"/"+key, nil)
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
