package s3api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/chester-hill-solutions/stow-s3/internal/authority"
	"github.com/chester-hill-solutions/stow-s3/internal/runtime"
	"github.com/chester-hill-solutions/stow-s3/internal/storage"
)

const xmlNS = "http://s3.amazonaws.com/doc/2006-03-01/"

type s3Error struct {
	Code       string
	Message    string
	Resource   string
	StatusCode int
}

func (e s3Error) Error() string {
	return e.Message
}

func newRequestID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func writeError(w http.ResponseWriter, r *http.Request, err s3Error) {
	reqID := requestIDFromContext(r.Context())
	if reqID == "" {
		reqID = newRequestID()
	}
	w.Header().Set("Content-Type", "application/xml")
	w.Header().Set("x-amz-request-id", reqID)
	setCORS(w, r)
	w.WriteHeader(err.StatusCode)
	_ = xml.NewEncoder(w).Encode(errorResponse{
		Code:      err.Code,
		Message:   err.Message,
		Resource:  err.Resource,
		RequestID: reqID,
	})
}

type errorResponse struct {
	XMLName   xml.Name `xml:"Error"`
	Code      string   `xml:"Code"`
	Message   string   `xml:"Message"`
	Resource  string   `xml:"Resource"`
	RequestID string   `xml:"RequestId"`
}

// DefaultMaxRequestBytes bounds a single request body when Config does not set
// a limit. It matches the documented default agent-session request ceiling.
const DefaultMaxRequestBytes int64 = 8 * 1024 * 1024

// ReadTimeout bounds reading a whole request, and WriteTimeout bounds the
// response. They are generous enough for a large upload on a slow link while
// still preventing a stalled peer from holding a connection and goroutine
// indefinitely.
const (
	ReadTimeout  = 5 * time.Minute
	WriteTimeout = 5 * time.Minute
)

// isRequestTooLarge reports whether err came from the request body limit.
func isRequestTooLarge(err error) bool {
	var tooLarge *http.MaxBytesError
	return errors.As(err, &tooLarge)
}

func requestTooLargeError(resource string, limit int64) s3Error {
	return s3Error{
		Code:       "EntityTooLarge",
		Message:    fmt.Sprintf("Your proposed upload exceeds the maximum allowed object size of %d bytes", limit),
		Resource:   resource,
		StatusCode: http.StatusBadRequest,
	}
}

// bodyReadError maps a failure to read the request body. Reading the body is
// where the size limit is enforced, so an over-limit body surfaces here and must
// keep reporting EntityTooLarge rather than a generic read failure.
func bodyReadError(err error, resource string, limit int64) s3Error {
	if isRequestTooLarge(err) {
		var tooLarge *http.MaxBytesError
		_ = errors.As(err, &tooLarge)
		return requestTooLargeError(resource, tooLarge.Limit)
	}
	return s3Error{
		Code:       "InvalidArgument",
		Message:    fmt.Sprintf("could not read request body: %v", err),
		Resource:   resource,
		StatusCode: http.StatusBadRequest,
	}
}

// storageErrorCase maps one sentinel to the S3 error a client should see.
//
// This was a fourteen-case if/else chain, and adding the authority refusal
// pushed it over the complexity limit. A table is the right shape for it anyway:
// the entries differ only in which sentinel they match and what they say, so a
// new mapping is a new row rather than a new branch, and the order — which
// matters, because the first match wins — is visible at a glance.
type storageErrorCase struct {
	sentinel   error
	code       string
	message    string
	statusCode int
}

var storageErrorCases = []storageErrorCase{
	{storage.ErrBucketNotFound, "NoSuchBucket", "The specified bucket does not exist", http.StatusNotFound},
	{storage.ErrInvalidBucketName, "InvalidBucketName", "The specified bucket name is not valid", http.StatusBadRequest},
	{storage.ErrObjectNotFound, "NoSuchKey", "The specified key does not exist", http.StatusNotFound},
	{storage.ErrBucketNotEmpty, "BucketNotEmpty", "The bucket you tried to delete is not empty", http.StatusConflict},
	{storage.ErrInvalidKey, "InvalidArgument", "Invalid object key", http.StatusBadRequest},
	{storage.ErrInvalidPart, "InvalidPart", "One or more of the specified parts could not be found", http.StatusBadRequest},
	{storage.ErrUploadNotFound, "NoSuchUpload", "The specified multipart upload does not exist", http.StatusNotFound},
	{storage.ErrNoSuchUpload, "NoSuchUpload", "The specified multipart upload does not exist", http.StatusNotFound},
	{storage.ErrPreconditionFailed, "PreconditionFailed", "At least one of the pre-conditions you specified did not hold", http.StatusPreconditionFailed},
	{runtime.ErrQuotaExceeded, "InsufficientStorage", "The runtime storage quota was exceeded", http.StatusInsufficientStorage},
	{runtime.ErrClosed, "ServiceUnavailable", "The runtime is closed", http.StatusServiceUnavailable},
	{runtime.ErrInvalidListLimit, "InvalidArgument", "The list limit is invalid", http.StatusBadRequest},
	{runtime.ErrMultipartUnsupported, "NotImplemented", "Multipart operations are not supported by this runtime profile", http.StatusNotImplemented},
}

func mapStorageError(err error, resource string) s3Error {
	// Two cases have to read the error rather than merely recognise it, so they
	// stay ahead of the table.
	if isRequestTooLarge(err) {
		var tooLarge *http.MaxBytesError
		_ = errors.As(err, &tooLarge)
		return requestTooLargeError(resource, tooLarge.Limit)
	}
	var denied *authority.ErrNotAuthorized
	if errors.As(err, &denied) {
		// A refusal is 403 AccessDenied, which is what S3 returns and what an
		// SDK feature-gate expects. Falling through to InternalError would
		// misreport a permission decision as a server fault and invite a retry
		// of something that can never succeed.
		//
		// The message names the operation and nothing about the environment, so
		// a caller learns what it may not do without learning what it may.
		return s3Error{
			Code:       "AccessDenied",
			Message:    fmt.Sprintf("Access Denied: %s is not permitted by this environment", denied.Operation),
			Resource:   resource,
			StatusCode: http.StatusForbidden,
		}
	}
	for _, mapping := range storageErrorCases {
		if errors.Is(err, mapping.sentinel) {
			return s3Error{
				Code:       mapping.code,
				Message:    mapping.message,
				Resource:   resource,
				StatusCode: mapping.statusCode,
			}
		}
	}
	return s3Error{Code: "InternalError", Message: "internal storage error", Resource: resource, StatusCode: http.StatusInternalServerError}
}

func writeXML(w http.ResponseWriter, r *http.Request, status int, v any) {
	reqID := requestIDFromContext(r.Context())
	if reqID == "" {
		reqID = newRequestID()
	}
	w.Header().Set("Content-Type", "application/xml")
	w.Header().Set("x-amz-request-id", reqID)
	setCORS(w, r)
	w.WriteHeader(status)
	enc := xml.NewEncoder(w)
	if _, ok := v.(struct{ XMLName xml.Name }); ok {
		_, _ = fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>`+"\n")
	}
	_ = enc.Encode(v)
}

func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

type ctxKey string

const requestIDKey ctxKey = "requestID"

func withRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

func requestIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(requestIDKey).(string); ok {
		return v
	}
	return ""
}
