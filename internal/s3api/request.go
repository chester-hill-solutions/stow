package s3api

import (
	"bytes"
	"crypto/md5"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/chester-hill-solutions/stow/internal/storage"
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

func checksumFromRequest(r *http.Request) (string, string, error) {
	algorithm, providedValue, err := checksumHeaderValues(r)
	if err != nil || algorithm == "" {
		return algorithm, providedValue, err
	}
	data, err := readAndRestoreBody(r)
	if err != nil {
		return "", "", err
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

func readAndRestoreBody(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	data, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(data))
	r.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(data)), nil
	}
	return data, nil
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
