package s3api_test

import (
	"bytes"
	"encoding/xml"
	"io"
	"net/http"
	"strings"
	"testing"
)

type multipartFixture struct {
	t        *testing.T
	baseURL  string
	uploadID string
}

func newMultipartFixture(t *testing.T, baseURL string) *multipartFixture {
	t.Helper()
	fixture := &multipartFixture{t: t, baseURL: baseURL}
	fixture.expectStatus(fixture.request(http.MethodPut, "/multipart-bucket", ""), http.StatusOK, "create bucket")
	resp := fixture.request(http.MethodPost, "/multipart-bucket/object.bin?uploads=", "")
	var initiated struct {
		UploadID string `xml:"UploadId"`
	}
	fixture.decode(resp, &initiated)
	if initiated.UploadID == "" {
		t.Fatal("initiate response did not include upload id")
	}
	fixture.uploadID = initiated.UploadID
	return fixture
}

func (f *multipartFixture) request(method, path, body string) *http.Response {
	f.t.Helper()
	req, err := http.NewRequest(method, f.baseURL+path, strings.NewReader(body))
	if err != nil {
		f.t.Fatalf("new request: %v", err)
	}
	if method == http.MethodPut || method == http.MethodPost {
		req.Header.Set("Content-Type", "application/octet-stream")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatalf("request %s %s: %v", method, path, err)
	}
	return resp
}

func (f *multipartFixture) requestWithBody(req *http.Request) *http.Response {
	f.t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatalf("request %s %s: %v", req.Method, req.URL, err)
	}
	return resp
}

func (f *multipartFixture) expectStatus(resp *http.Response, want int, label string) {
	f.t.Helper()
	if resp.StatusCode == want {
		resp.Body.Close()
		return
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	f.t.Fatalf("%s status = %d, want %d: %s", label, resp.StatusCode, want, body)
}

func (f *multipartFixture) decode(resp *http.Response, target interface{}) {
	f.t.Helper()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		f.t.Fatalf("response status = %d: %s", resp.StatusCode, body)
	}
	if err := xml.NewDecoder(resp.Body).Decode(target); err != nil {
		f.t.Fatalf("decode response: %v", err)
	}
}

func (f *multipartFixture) listPath() string {
	return "/multipart-bucket/object.bin?uploadId=" + f.uploadID
}

func (f *multipartFixture) listParts() {
	f.t.Helper()
	f.expectStatus(f.request(http.MethodGet, f.listPath(), ""), http.StatusOK, "list parts")
}

func (f *multipartFixture) putPart(part []byte) string {
	f.t.Helper()
	resp := f.request(http.MethodPut, f.listPath()+"&partNumber=1", string(part))
	var uploaded struct {
		ETag string `xml:"ETag"`
	}
	f.decode(resp, &uploaded)
	return uploaded.ETag
}

func (f *multipartFixture) complete(etag string) {
	f.t.Helper()
	body := `<CompleteMultipartUpload><Part><PartNumber>1</PartNumber><ETag>` + etag + `</ETag></Part></CompleteMultipartUpload>`
	f.expectStatus(f.request(http.MethodPost, f.listPath(), body), http.StatusOK, "complete")
}

func TestMultipartRouteValidatesObjectIdentity(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	fixture := newMultipartFixture(t, ts.URL)

	wrongPath := "/multipart-bucket/other.bin?uploadId=" + fixture.uploadID
	fixture.expectStatus(fixture.request(http.MethodGet, wrongPath, ""), http.StatusNotFound, "wrong object route")
	fixture.listParts()

	badPart, err := http.NewRequest(http.MethodPut, ts.URL+fixture.listPath()+"&partNumber=1", strings.NewReader("x"))
	if err != nil {
		t.Fatalf("new invalid part request: %v", err)
	}
	badPart.ContentLength = -1
	fixture.expectStatus(fixture.requestWithBody(badPart), http.StatusBadRequest, "chunked upload part")

	etag := fixture.putPart(bytes.Repeat([]byte("a"), 5*1024*1024))
	fixture.complete(etag)
}
