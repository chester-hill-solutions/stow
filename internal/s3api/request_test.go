package s3api_test

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestPutObjectRejectsInvalidContentLength(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	req, err := http.NewRequest(http.MethodPut, ts.URL+"/length-bucket", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	for _, testCase := range []struct {
		name          string
		contentLength int64
	}{
		{name: "missing", contentLength: -1},
		{name: "empty without header", contentLength: 0},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPut, ts.URL+"/length-bucket/key", strings.NewReader("body"))
			if err != nil {
				t.Fatal(err)
			}
			req.ContentLength = testCase.contentLength
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				body, _ := io.ReadAll(resp.Body)
				t.Fatalf("status = %d, want 400: %s", resp.StatusCode, body)
			}
		})
	}
}
