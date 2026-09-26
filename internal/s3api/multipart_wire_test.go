package s3api_test

// The multipart surface at the wire: what a client is told when it finishes an
// upload.
//
// The handlers here translate a store's answer into a status code, and a
// translation is where a discarded error turns into a reported success. The
// shared request helpers live in wire_contract_test.go.

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/chester-hill-solutions/stow-s3/internal/storage"
)

// unreadablePartsStore is a store whose ListParts fails while its other
// operations work, which is the condition the completion handler mishandled: it
// discarded the error, leaving the part-size map empty and skipping the
// minimum-part-size check without saying so.
type unreadablePartsStore struct {
	storage.Store
	failure error
}

func (s unreadablePartsStore) ListParts(context.Context, string) ([]storage.PartInfo, error) {
	return nil, s.failure
}

// CompleteMultipartUpload succeeds, so that a 200 in the test below can only
// come from the handler proceeding in spite of the listing failure rather than
// from some later check happening to fail.
func (s unreadablePartsStore) CompleteMultipartUpload(_ context.Context, uploadID string, _ []storage.PartInfo) (*storage.ObjectMeta, error) {
	return &storage.ObjectMeta{Bucket: "uploads", Key: "object.bin", ETag: `"stub"`, Size: 1}, nil
}

// completeOverWire issues a CompleteMultipartUpload naming the given part
// numbers, and returns the status and body.
func completeOverWire(t *testing.T, ts *httptest.Server, ref objectRef, uploadID string, partNumbers ...int) (int, string) {
	t.Helper()
	var request strings.Builder
	request.WriteString("<CompleteMultipartUpload>")
	for _, number := range partNumbers {
		fmt.Fprintf(&request, "<Part><PartNumber>%d</PartNumber><ETag>\"etag\"</ETag></Part>", number)
	}
	request.WriteString("</CompleteMultipartUpload>")
	body := request.String()

	req, err := http.NewRequest(http.MethodPost, ts.URL+ref.path()+"?uploadId="+uploadID, strings.NewReader(body))
	if err != nil {
		t.Fatalf("build complete: %v", err)
	}
	req.ContentLength = int64(len(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(data)
}

// uploadPartsOverWire starts a multipart upload and uploads count parts of the
// given size, returning the upload ID and each part's ETag.
func uploadPartsOverWire(t *testing.T, ts *httptest.Server, ref objectRef, count, size int) (string, []string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, ts.URL+ref.path()+"?uploads", nil)
	if err != nil {
		t.Fatalf("build create upload: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("create upload: %v", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	var initiated struct {
		UploadID string `xml:"UploadId"`
	}
	if err := xml.Unmarshal(data, &initiated); err != nil {
		t.Fatalf("parse create upload %q: %v", data, err)
	}
	if initiated.UploadID == "" {
		t.Fatalf("create upload returned no id: %s", data)
	}

	etags := make([]string, 0, count)
	payload := strings.Repeat("x", size)
	for number := 1; number <= count; number++ {
		req, err := http.NewRequest(http.MethodPut, ts.URL+ref.path()+"?partNumber="+strconv.Itoa(number)+"&uploadId="+initiated.UploadID, strings.NewReader(payload))
		if err != nil {
			t.Fatalf("build upload part: %v", err)
		}
		req.ContentLength = int64(len(payload))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("upload part %d: %v", number, err)
		}
		partData, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var part struct {
			ETag string `xml:"ETag"`
		}
		if err := xml.Unmarshal(partData, &part); err != nil {
			t.Fatalf("parse upload part %d %q: %v", number, partData, err)
		}
		etags = append(etags, part.ETag)
	}
	return initiated.UploadID, etags
}

// A failure to list the upload's parts is reported, not absorbed.
//
// The completion handler called ListParts to build the map it uses to enforce
// S3's 5 MiB minimum on non-final parts, and discarded the error. An empty map
// made every lookup miss, so the size check passed for every part and a client
// could complete an upload made of 1-byte parts - the one thing that check
// exists to prevent. The failure was invisible: the request carried on to
// CompleteMultipartUpload and reported success.
//
// The store here fails ListParts and completes successfully, so the only correct
// outcome is a refusal. A 200 is the bug, and it is reachable rather than
// theoretical: any I/O error reading the upload directory produced it.
func TestCompleteMultipartUploadReportsAFailedPartListing(t *testing.T) {
	cases := []struct {
		name       string
		failure    error
		wantStatus int
		wantInBody string
	}{
		// A store error with a mapping is reported as that error, so a client
		// asking about an upload that is not there is told so at the point it
		// asks rather than after a wasted round trip through completion.
		{"mapped store error", storage.ErrUploadNotFound, http.StatusNotFound, "NoSuchUpload"},
		// An unmapped error is a server fault, and 500 is the honest answer: the
		// request was well formed and the store broke. What must not happen is
		// the error disappearing and the request being reported as a success.
		{"unmapped store error", errors.New("the upload directory could not be read"), http.StatusInternalServerError, "InternalError"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := newStoreServer(t, unreadablePartsStore{
				Store:   storage.NewMemoryStore(),
				failure: tc.failure,
			})
			createBucketOverWire(t, ts, "uploads")
			uploadID, _ := uploadPartsOverWire(t, ts, objectRef{"uploads", "object.bin"}, 2, 16)

			status, body := completeOverWire(t, ts, objectRef{"uploads", "object.bin"}, uploadID, 1, 2)
			if status != tc.wantStatus {
				t.Fatalf("complete = %d, want %d: %s", status, tc.wantStatus, body)
			}
			if !strings.Contains(body, tc.wantInBody) {
				t.Fatalf("complete body = %s, want it to report %s", body, tc.wantInBody)
			}
		})
	}
}

// The minimum part size is enforced on non-final parts: two 16-byte parts, the
// first of which is not final, are refused.
func TestCompleteMultipartUploadEnforcesTheMinimumPartSize(t *testing.T) {
	ts := newStoreServer(t, storage.NewMemoryStore())
	createBucketOverWire(t, ts, "uploads")
	uploadID, _ := uploadPartsOverWire(t, ts, objectRef{"uploads", "object.bin"}, 2, 16)

	status, body := completeOverWire(t, ts, objectRef{"uploads", "object.bin"}, uploadID, 1, 2)
	if status != http.StatusBadRequest {
		t.Fatalf("complete of two 16-byte parts = %d, want 400: %s", status, body)
	}
	if !strings.Contains(body, "EntityTooSmall") {
		t.Fatalf("complete body = %s, want EntityTooSmall", body)
	}
}

// The final part is exempt, as it is in S3: the minimum applies to the parts a
// client intends to keep appending to, and refusing the last one would make
// every small upload impossible. A single part is always final, so a lone
// undersized part completes.
func TestCompleteMultipartUploadExemptsTheFinalPartFromTheMinimum(t *testing.T) {
	ts := newStoreServer(t, storage.NewMemoryStore())
	createBucketOverWire(t, ts, "uploads")
	uploadID, etags := uploadPartsOverWire(t, ts, objectRef{"uploads", "object.bin"}, 1, 16)

	var request strings.Builder
	request.WriteString("<CompleteMultipartUpload>")
	fmt.Fprintf(&request, "<Part><PartNumber>1</PartNumber><ETag>%s</ETag></Part>", etags[0])
	request.WriteString("</CompleteMultipartUpload>")
	body := request.String()

	req, err := http.NewRequest(http.MethodPost, ts.URL+objectRef{"uploads", "object.bin"}.path()+"?uploadId="+uploadID, strings.NewReader(body))
	if err != nil {
		t.Fatalf("build complete: %v", err)
	}
	req.ContentLength = int64(len(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("complete of a single small final part = %d, want 200: %s", resp.StatusCode, data)
	}
}
