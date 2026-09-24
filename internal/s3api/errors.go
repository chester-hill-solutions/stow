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

	"github.com/chester-hill-solutions/stow/internal/storage"
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

func mapStorageError(err error, resource string) s3Error {
	switch {
	case errors.Is(err, storage.ErrBucketNotFound):
		return s3Error{Code: "NoSuchBucket", Message: "The specified bucket does not exist", Resource: resource, StatusCode: http.StatusNotFound}
	case errors.Is(err, storage.ErrInvalidBucketName):
		return s3Error{Code: "InvalidBucketName", Message: "The specified bucket name is not valid", Resource: resource, StatusCode: http.StatusBadRequest}
	case errors.Is(err, storage.ErrObjectNotFound):
		return s3Error{Code: "NoSuchKey", Message: "The specified key does not exist", Resource: resource, StatusCode: http.StatusNotFound}
	case errors.Is(err, storage.ErrBucketNotEmpty):
		return s3Error{Code: "BucketNotEmpty", Message: "The bucket you tried to delete is not empty", Resource: resource, StatusCode: http.StatusConflict}
	case errors.Is(err, storage.ErrInvalidKey):
		return s3Error{Code: "InvalidArgument", Message: "Invalid object key", Resource: resource, StatusCode: http.StatusBadRequest}
	case errors.Is(err, storage.ErrInvalidPart):
		return s3Error{Code: "InvalidPart", Message: "One or more of the specified parts could not be found", Resource: resource, StatusCode: http.StatusBadRequest}
	case errors.Is(err, storage.ErrUploadNotFound), errors.Is(err, storage.ErrNoSuchUpload):
		return s3Error{Code: "NoSuchUpload", Message: "The specified multipart upload does not exist", Resource: resource, StatusCode: http.StatusNotFound}
	case errors.Is(err, storage.ErrPreconditionFailed):
		return s3Error{Code: "PreconditionFailed", Message: "At least one of the pre-conditions you specified did not hold", Resource: resource, StatusCode: http.StatusPreconditionFailed}
	default:
		return s3Error{Code: "InternalError", Message: err.Error(), Resource: resource, StatusCode: http.StatusInternalServerError}
	}
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
