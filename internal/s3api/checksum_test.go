package s3api_test

import (
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/chester-hill-solutions/stow-s3/internal/storage"
)

func TestPutObjectChecksumAlgorithms(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	create := func(bucket string) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPut, ts.URL+"/"+bucket, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("create bucket status = %d", resp.StatusCode)
		}
	}
	create("checksum-bucket")
	body := "checksum-body"
	for _, algorithm := range []string{"CRC32", "CRC32C", "SHA1", "SHA256"} {
		t.Run(algorithm, func(t *testing.T) {
			value, err := storage.ComputeChecksum(algorithm, []byte(body))
			if err != nil {
				t.Fatal(err)
			}
			key := strings.ToLower(algorithm)
			req, _ := http.NewRequest(http.MethodPut, ts.URL+"/checksum-bucket/"+key, strings.NewReader(body))
			req.Header.Set("x-amz-checksum-"+strings.ToLower(algorithm), value)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				payload, _ := io.ReadAll(resp.Body)
				t.Fatalf("status = %d: %s", resp.StatusCode, payload)
			}
			if got := resp.Header.Get("x-amz-checksum-" + strings.ToLower(algorithm)); got != value {
				t.Fatalf("response checksum = %q, want %q", got, value)
			}
		})
	}
}

func TestPutObjectRejectsBadAndUnknownChecksums(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/checksum-bucket", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	bad := base64.StdEncoding.EncodeToString([]byte{0, 0, 0, 0})
	for _, testCase := range []struct {
		name   string
		header string
		value  string
	}{
		{name: "bad digest", header: "x-amz-checksum-crc32", value: bad},
		{name: "unknown algorithm", header: "x-amz-checksum-sha512", value: bad},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodPut, ts.URL+"/checksum-bucket/"+testCase.name, strings.NewReader("body"))
			req.Header.Set(testCase.header, testCase.value)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", resp.StatusCode)
			}
		})
	}
}
