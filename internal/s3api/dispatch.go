package s3api

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/chester-hill-solutions/stow-s3/internal/storage"
)

func (s *Server) dispatch(ctx context.Context, w http.ResponseWriter, r *http.Request, route routeInfo) {
	q := r.URL.Query()
	if code, message, ok := unsupportedSemanticMarker(r, q); ok {
		status := http.StatusNotImplemented
		if code == "InvalidArgument" {
			status = http.StatusBadRequest
		}
		writeError(w, r, s3Error{Code: code, Message: message, Resource: resourcePath(route.bucket, route.key), StatusCode: status})
		return
	}

	switch {
	case route.bucket == "":
		s.dispatchRoot(ctx, w, r)
	case route.key == "":
		s.dispatchBucket(ctx, w, r, route.bucket, q)
	default:
		s.dispatchObject(ctx, w, r, route)
	}
}

func (s *Server) dispatchRoot(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.URL.Path == "/" {
		s.handleListBuckets(ctx, w, r)
		return
	}
	writeError(w, r, s3Error{Code: "InvalidRequest", Message: "Invalid request", StatusCode: http.StatusBadRequest})
}

func (s *Server) dispatchBucket(ctx context.Context, w http.ResponseWriter, r *http.Request, bucket string, q url.Values) {
	switch r.Method {
	case http.MethodPut:
		s.handleCreateBucket(ctx, w, r, bucket)
	case http.MethodHead:
		s.handleHeadBucket(ctx, w, r, bucket)
	case http.MethodDelete:
		if q.Has("delete") {
			writeError(w, r, s3Error{Code: "NotImplemented", Message: "Not implemented", StatusCode: http.StatusNotImplemented})
			return
		}
		s.handleDeleteBucket(ctx, w, r, bucket)
	case http.MethodGet:
		if q.Get("list-type") == "2" {
			s.handleListObjectsV2(ctx, w, r, bucket, q)
			return
		}
		if q.Has("uploads") {
			if s.requireMultipart(w, r, "/"+bucket) {
				return
			}
			s.handleListMultipartUploads(ctx, w, r, bucket, q)
			return
		}
		writeError(w, r, s3Error{Code: "InvalidRequest", Message: "Invalid request", StatusCode: http.StatusBadRequest})
	case http.MethodPost:
		if q.Has("delete") {
			s.handleDeleteObjects(ctx, w, r, bucket)
			return
		}
		writeError(w, r, s3Error{Code: "InvalidRequest", Message: "Invalid request", StatusCode: http.StatusBadRequest})
	default:
		writeError(w, r, s3Error{Code: "MethodNotAllowed", Message: "Method not allowed", StatusCode: http.StatusMethodNotAllowed})
	}
}

func (s *Server) dispatchObject(ctx context.Context, w http.ResponseWriter, r *http.Request, route routeInfo) {
	bucket, key := route.bucket, route.key
	if s.dispatchMultipartObject(ctx, w, r, route) {
		return
	}
	switch r.Method {
	case http.MethodPut:
		if copySrc := r.Header.Get("x-amz-copy-source"); copySrc != "" {
			s.handleCopyObject(ctx, w, r, bucket, key, copySrc)
			return
		}
		s.handlePutObject(ctx, w, r, bucket, key)
	case http.MethodGet:
		s.handleGetObject(ctx, w, r, bucket, key)
	case http.MethodHead:
		s.handleHeadObject(ctx, w, r, bucket, key)
	case http.MethodDelete:
		s.handleDeleteObject(ctx, w, r, bucket, key)
	default:
		writeError(w, r, s3Error{Code: "MethodNotAllowed", Message: "Method not allowed", StatusCode: http.StatusMethodNotAllowed})
	}
}

func (s *Server) dispatchMultipartObject(ctx context.Context, w http.ResponseWriter, r *http.Request, route routeInfo) bool {
	q := r.URL.Query()
	if !isMultipartRequest(r, q) {
		return false
	}
	if s.requireMultipart(w, r, resourcePath(route.bucket, route.key)) {
		return true
	}
	bucket, key := route.bucket, route.key
	if r.Method == http.MethodPost && q.Has("uploads") {
		s.handleCreateMultipartUpload(ctx, w, r, bucket, key)
		return true
	}
	if r.Method == http.MethodPut && q.Has("partNumber") && q.Has("uploadId") {
		s.handleUploadPart(ctx, w, r, bucket, key, q)
		return true
	}
	if r.Method == http.MethodPost && q.Has("uploadId") {
		s.handleCompleteMultipartUpload(ctx, w, r, bucket, key, q.Get("uploadId"))
		return true
	}
	if r.Method == http.MethodDelete && q.Has("uploadId") {
		s.handleAbortMultipartUpload(ctx, w, r, bucket, key, q.Get("uploadId"))
		return true
	}
	if r.Method == http.MethodGet && q.Has("uploadId") {
		s.handleListParts(ctx, w, r, bucket, key, q.Get("uploadId"))
		return true
	}
	return false
}

// isMultipartRequest reports whether a request addresses an upload rather than
// the object itself, so a store without multipart can be refused before the
// route is dispatched.
func isMultipartRequest(r *http.Request, q url.Values) bool {
	if q.Has("uploadId") {
		return true
	}
	return r.Method == http.MethodPost && q.Has("uploads") ||
		r.Method == http.MethodPut && q.Has("partNumber")
}

// requireMultipart writes the refusal and reports whether it did. The capability
// is read from the store, so the answer is known before a byte is read rather
// than partway through an upload.
func (s *Server) requireMultipart(w http.ResponseWriter, r *http.Request, resource string) bool {
	if s.multipart != nil {
		return false
	}
	writeError(w, r, mapStorageError(storage.ErrMultipartUnsupported, resource))
	return true
}

func unsupportedSemanticMarker(r *http.Request, q url.Values) (code, message string, ok bool) {
	for _, marker := range []string{
		"versioning", "acl", "policy", "lifecycle", "replication", "notification",
		"tagging", "website", "logging", "accelerate", "requestPayment", "encryption",
		"object-lock", "inventory", "metrics", "analytics", "intelligent-tiering", "select",
	} {
		if q.Has(marker) {
			return "NotImplemented", "operation is not implemented", true
		}
	}
	if q.Has("versionId") {
		return "InvalidArgument", "versionId is not supported", true
	}
	if strings.EqualFold(r.Header.Get("X-Amz-Server-Side-Encryption"), "aws:kms") ||
		strings.EqualFold(r.Header.Get("X-Amz-Server-Side-Encryption"), "aws:kms:dsse") {
		return "InvalidArgument", "KMS encryption is not supported", true
	}
	return "", "", false
}
