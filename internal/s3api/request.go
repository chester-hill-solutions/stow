package s3api

import (
	"bytes"
	"crypto/md5"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strings"
)

func enforceContentLength(r *http.Request) error {
	if r.ContentLength < 0 {
		return fmt.Errorf("Content-Length required")
	}
	var data []byte
	var err error
	if r.Body != nil {
		data, err = io.ReadAll(r.Body)
		if err != nil {
			return err
		}
		_ = r.Body.Close()
	}
	if int64(len(data)) != r.ContentLength {
		return fmt.Errorf("Content-Length mismatch")
	}
	r.Body = io.NopCloser(bytes.NewReader(data))
	r.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(data)), nil
	}
	return nil
}

func verifyContentMD5(r *http.Request) error {
	encoded := strings.TrimSpace(r.Header.Get("Content-MD5"))
	if encoded == "" {
		return nil
	}
	expected, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return fmt.Errorf("invalid Content-MD5: %w", err)
	}
	var data []byte
	if r.Body != nil {
		data, err = io.ReadAll(r.Body)
		if err != nil {
			return err
		}
		_ = r.Body.Close()
	}
	r.Body = io.NopCloser(bytes.NewReader(data))
	r.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(data)), nil
	}
	actual := md5.Sum(data)
	if !bytes.Equal(expected, actual[:]) {
		return fmt.Errorf("Content-MD5 mismatch")
	}
	return nil
}

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
	r.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(data)), nil
	}
	return nil
}
