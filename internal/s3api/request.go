package s3api

import (
	"bytes"
	"io"
	"net/http"
	"strings"
)

// prepareRequestForAuth makes request bodies replayable for SigV4 payload verification.
// AWS SDK clients sign bodyless requests (CreateBucket, DeleteObject, etc.) with the
// empty payload hash but do not set http.Request.GetBody on the server side.
func prepareRequestForAuth(r *http.Request) error {
	if r.GetBody != nil {
		return nil
	}

	if r.Body == nil || r.ContentLength == 0 {
		if r.Body != nil {
			_, _ = io.Copy(io.Discard, r.Body)
			_ = r.Body.Close()
		}
		r.Body = http.NoBody
		r.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(nil)), nil
		}
		return nil
	}

	if strings.EqualFold(r.Header.Get("X-Amz-Content-Sha256"), "UNSIGNED-PAYLOAD") {
		return nil
	}

	data, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(data))
	r.ContentLength = int64(len(data))
	r.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(data)), nil
	}
	return nil
}
