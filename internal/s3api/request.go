package s3api

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/chester-hill-solutions/stow-s3/internal/storage"
)

// bodyCacheKey carries a per-request holder for the materialised body.
//
// A pointer is stored rather than the bytes because a request context is
// immutable, and the point of this is that the stage which first reads the body
// publishes it for the stages after it. ServeHTTP installs one holder for every
// request, so a handler gets the same cache whether or not authentication ran.
type bodyCache struct {
	data []byte
	read bool
}

// bodyReader exposes the already-materialised request body to the store, so the
// bytes this layer read for the SigV4, Content-Length, Content-MD5 and checksum
// checks are the same bytes the store takes, instead of being read a second time
// into a full-size buffer on the way down.
type bodyReader struct {
	*bytes.Reader
	data []byte
}

func newBodyReader(data []byte) bodyReader {
	return bodyReader{Reader: bytes.NewReader(data), data: data}
}

// Bytes implements storage.ByteReader. The returned slice is the request's body
// buffer and must be treated as read-only by the consumer.
func (b bodyReader) Bytes() []byte { return b.data }

type bodyCacheKey struct{}

// withBodyCache returns a request carrying a fresh body cache.
func withBodyCache(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), bodyCacheKey{}, &bodyCache{}))
}

// restoreBody puts the bytes back on the request so a later stage, including the
// store, can read the body again without this layer re-reading the socket.
// bytes.NewReader wraps the slice rather than copying it.
func restoreBody(r *http.Request, data []byte) {
	r.Body = io.NopCloser(bytes.NewReader(data))
	r.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(data)), nil
	}
}

// requestBody returns the whole request body, reading the socket at most once
// per request.
//
// Every consumer of the body in this package needs all of it: SigV4 payload
// verification, the Content-Length comparison, Content-MD5, the checksum
// algorithms, and the store itself. Reading it separately for each one allocated
// a fresh full-size copy per consumer, which is where most of the measured
// per-MiB memory amplification came from.
func requestBody(r *http.Request) ([]byte, error) {
	cache, ok := r.Context().Value(bodyCacheKey{}).(*bodyCache)
	if ok && cache.read {
		return cache.data, nil
	}
	if r.Body == nil {
		if ok {
			cache.read = true
		}
		return nil, nil
	}
	data, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	_ = r.Body.Close()
	restoreBody(r, data)
	if ok {
		cache.data = data
		cache.read = true
	}
	return data, nil
}

func enforceContentLength(r *http.Request, data []byte) error {
	if r.Header.Get("Content-Length") == "" || r.ContentLength < 0 {
		return fmt.Errorf("Content-Length required")
	}
	if int64(len(data)) != r.ContentLength {
		return fmt.Errorf("Content-Length mismatch")
	}
	return nil
}

func verifyContentMD5(r *http.Request, data []byte) error {
	encoded := strings.TrimSpace(r.Header.Get("Content-MD5"))
	if encoded == "" {
		return nil
	}
	expected, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return fmt.Errorf("invalid Content-MD5: %w", err)
	}
	actual := md5.Sum(data)
	if !bytes.Equal(expected, actual[:]) {
		return storage.ErrMD5Mismatch
	}
	return nil
}

var checksumHeaderNames = map[string]string{
	"CRC32":  "x-amz-checksum-crc32",
	"CRC32C": "x-amz-checksum-crc32c",
	"SHA1":   "x-amz-checksum-sha1",
	"SHA256": "x-amz-checksum-sha256",
}

func checksumHeaderValues(r *http.Request) (string, string, error) {
	algorithm, err := requestedChecksumAlgorithm(r)
	if err != nil {
		return "", "", err
	}
	providedAlgorithm, providedValue, err := providedChecksum(r)
	if err != nil {
		return "", "", err
	}
	if algorithm == "" {
		algorithm = providedAlgorithm
	}
	if algorithm == "" {
		return "", "", nil
	}
	if providedAlgorithm != "" && providedAlgorithm != algorithm {
		return "", "", fmt.Errorf("checksum header does not match algorithm")
	}
	if providedValue != "" {
		if _, err := base64.StdEncoding.DecodeString(providedValue); err != nil {
			return "", "", fmt.Errorf("invalid checksum encoding: %w", err)
		}
	}
	return algorithm, providedValue, nil
}

func requestedChecksumAlgorithm(r *http.Request) (string, error) {
	algorithm := storage.NormalizeChecksumAlgorithm(r.Header.Get("x-amz-sdk-checksum-algorithm"))
	headerAlgorithm := storage.NormalizeChecksumAlgorithm(r.Header.Get("x-amz-checksum-algorithm"))
	if headerAlgorithm == "" {
		return algorithm, nil
	}
	if algorithm != "" && algorithm != headerAlgorithm {
		return "", fmt.Errorf("conflicting checksum algorithms")
	}
	return headerAlgorithm, nil
}

func providedChecksum(r *http.Request) (string, string, error) {
	providedAlgorithm := ""
	providedValue := ""
	for candidate, headerName := range checksumHeaderNames {
		value := r.Header.Get(headerName)
		if value == "" {
			continue
		}
		if providedAlgorithm != "" {
			return "", "", fmt.Errorf("multiple checksum headers")
		}
		providedAlgorithm = candidate
		providedValue = value
	}
	if err := validateChecksumHeaderNames(r); err != nil {
		return "", "", err
	}
	return providedAlgorithm, providedValue, nil
}

func validateChecksumHeaderNames(r *http.Request) error {
	for name := range r.Header {
		if strings.EqualFold(name, "x-amz-checksum-algorithm") {
			continue
		}
		if !strings.HasPrefix(strings.ToLower(name), "x-amz-checksum-") {
			continue
		}
		if _, known := checksumHeaderValuesByName(name); !known {
			return fmt.Errorf("unsupported checksum algorithm %q", strings.TrimPrefix(strings.ToLower(name), "x-amz-checksum-"))
		}
	}
	return nil
}

func checksumHeaderValuesByName(name string) (string, bool) {
	name = strings.ToLower(name)
	for algorithm, headerName := range checksumHeaderNames {
		if name == headerName {
			return algorithm, true
		}
	}
	return "", false
}

func checksumFromRequest(r *http.Request, data []byte) (string, string, error) {
	algorithm, providedValue, err := checksumHeaderValues(r)
	if err != nil || algorithm == "" {
		return algorithm, providedValue, err
	}
	computed, err := storage.ComputeChecksum(algorithm, data)
	if err != nil {
		return "", "", err
	}
	if providedValue != "" && providedValue != computed {
		return "", "", storage.ErrChecksumMismatch
	}
	return algorithm, computed, nil
}

// prepareRequestForAuth makes request bodies replayable for SigV4 payload verification.
// AWS SDK clients sign bodyless requests (CreateBucket, DeleteObject, etc.) with the
// empty payload hash but do not set http.Request.GetBody on the server side.
//
// It returns a request carrying the body when it had to read one, so the
// handlers downstream reuse those bytes instead of each reading the socket
// again. The bytes are still restored onto the request, so the store reads them
// from r.Body as before.
func prepareRequestForAuth(r *http.Request) (*http.Request, error) {
	if r.GetBody != nil {
		return r, nil
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
		return r, nil
	}

	if strings.EqualFold(r.Header.Get("X-Amz-Content-Sha256"), "UNSIGNED-PAYLOAD") {
		return r, nil
	}

	prepared, err := requestBody(r)
	if err != nil {
		return r, err
	}
	restoreBody(r, prepared)
	return r, nil
}
