package s3api_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/chester-hill-solutions/stow-s3/internal/s3api"
	"github.com/chester-hill-solutions/stow-s3/internal/storage"
)

// objectModelOnly is a store with the object model and no multipart half. It is
// what a caller-supplied store that declined the optional interface looks like
// to the server, and multipart is optional in the contract, so the server has to
// have an answer for its absence.
type objectModelOnly struct{ storage.Store }

func TestMultipartIsRefusedAtTheRouteWhenTheStoreCannotServeIt(t *testing.T) {
	local := &objectModelOnly{Store: storage.NewMemoryStore()}
	if err := local.CreateBucket(t.Context(), "bucket"); err != nil {
		t.Fatalf("seed bucket: %v", err)
	}

	srv, err := s3api.New(s3api.Config{
		Store: local,
		Auth:  s3api.DevBypass,
		Host:  "127.0.0.1",
		Port:  0,
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	server := httptest.NewServer(srv.Handler())
	t.Cleanup(server.Close)

	for _, tc := range []struct {
		name   string
		method string
		path   string
	}{
		{"create upload", http.MethodPost, "/bucket/key?uploads"},
		{"upload part", http.MethodPut, "/bucket/key?partNumber=1&uploadId=abc"},
		{"complete upload", http.MethodPost, "/bucket/key?uploadId=abc"},
		{"abort upload", http.MethodDelete, "/bucket/key?uploadId=abc"},
		{"list parts", http.MethodGet, "/bucket/key?uploadId=abc"},
		{"list uploads", http.MethodGet, "/bucket?uploads"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(tc.method, server.URL+tc.path, nil)
			if err != nil {
				t.Fatalf("new request: %v", err)
			}
			resp, err := server.Client().Do(req)
			if err != nil {
				t.Fatalf("do: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusNotImplemented {
				t.Errorf("%s %s = %d, want 501", tc.method, tc.path, resp.StatusCode)
			}
		})
	}

	// The object model still answers, so the refusal is the capability.
	req, err := http.NewRequest(http.MethodPut, server.URL+"/bucket/plain", nil)
	if err != nil {
		t.Fatalf("new put: %v", err)
	}
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("put on a store without multipart = %d, want 200", resp.StatusCode)
	}
}
