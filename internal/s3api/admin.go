package s3api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/chester-hill-solutions/stow/internal/storage"
	"github.com/chester-hill-solutions/stow/internal/version"
)

func (s *Server) handleAdmin(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/_stow/health":
		if r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	case "/_stow/status":
		if r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		s.writeStatus(w, r)
	case "/_stow/inspect":
		if r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		s.writeInspect(w, r)
	default:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
	}
}

func (s *Server) writeStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	buckets, _ := s.store.ListBuckets(ctx)
	objectCount := 0
	for _, b := range buckets {
		list, err := s.store.ListObjectsV2(ctx, b.Name, storage.ListOptions{MaxKeys: 10000})
		if err == nil {
			objectCount += len(list.Objects)
		}
	}
	addr := s.listenAddr
	if addr == "" {
		addr = s.config.Host
	}
	mode := s.config.Mode
	if mode == "" {
		mode = "local"
	}
	cachePolicy := s.config.CachePolicy
	if cachePolicy == "" {
		cachePolicy = "none"
	}
	writePolicy := s.config.WritePolicy
	if writePolicy == "" {
		writePolicy = "local-only"
	}
	payload := map[string]any{
		"mode":         mode,
		"listen":       addr,
		"region":       s.config.Region,
		"bucket_count": len(buckets),
		"object_count": objectCount,
		"cache_policy": cachePolicy,
		"write_policy": writePolicy,
		"uptime_sec":   int(time.Since(s.startTime).Seconds()),
		"version":      version.Version,
	}
	if s.config.UpstreamHost != "" {
		payload["upstream"] = s.config.UpstreamHost
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

func (s *Server) writeInspect(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	bucketFilter := r.URL.Query().Get("bucket")
	buckets, _ := s.store.ListBuckets(ctx)

	type bucketSnap struct {
		Name         string `json:"name"`
		ObjectCount  int    `json:"object_count"`
		CreationDate string `json:"creation_date"`
	}
	var snaps []bucketSnap
	for _, b := range buckets {
		if bucketFilter != "" && b.Name != bucketFilter {
			continue
		}
		list, err := s.store.ListObjectsV2(ctx, b.Name, storage.ListOptions{MaxKeys: 10000})
		count := 0
		if err == nil {
			count = len(list.Objects)
		}
		snaps = append(snaps, bucketSnap{
			Name:         b.Name,
			ObjectCount:  count,
			CreationDate: formatTime(b.CreationDate),
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"buckets":           snaps,
		"multipart_uploads": 0,
		"cache_hits":        0,
		"cache_misses":      0,
	})
}

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

	if route.bucket == "" {
		if r.Method == http.MethodGet && r.URL.Path == "/" {
			s.handleListBuckets(ctx, w, r)
			return
		}
		writeError(w, r, s3Error{Code: "InvalidRequest", Message: "Invalid request", StatusCode: http.StatusBadRequest})
		return
	}

	if route.key == "" {
		switch r.Method {
		case http.MethodPut:
			s.handleCreateBucket(ctx, w, r, route.bucket)
		case http.MethodHead:
			s.handleHeadBucket(ctx, w, r, route.bucket)
		case http.MethodDelete:
			if q.Has("delete") {
				writeError(w, r, s3Error{Code: "NotImplemented", Message: "Not implemented", StatusCode: http.StatusNotImplemented})
				return
			}
			s.handleDeleteBucket(ctx, w, r, route.bucket)
		case http.MethodGet:
			if q.Get("list-type") == "2" {
				s.handleListObjectsV2(ctx, w, r, route.bucket, q)
				return
			}
			if q.Has("uploads") {
				s.handleListMultipartUploads(ctx, w, r, route.bucket, q)
				return
			}
			writeError(w, r, s3Error{Code: "InvalidRequest", Message: "Invalid request", StatusCode: http.StatusBadRequest})
		case http.MethodPost:
			if q.Has("delete") {
				s.handleDeleteObjects(ctx, w, r, route.bucket)
				return
			}
			writeError(w, r, s3Error{Code: "InvalidRequest", Message: "Invalid request", StatusCode: http.StatusBadRequest})
		default:
			writeError(w, r, s3Error{Code: "MethodNotAllowed", Message: "Method not allowed", StatusCode: http.StatusMethodNotAllowed})
		}
		return
	}

	// Object-level operations
	if r.Method == http.MethodPost && q.Has("uploads") {
		s.handleCreateMultipartUpload(ctx, w, r, route.bucket, route.key)
		return
	}
	if r.Method == http.MethodPut && q.Has("partNumber") && q.Has("uploadId") {
		s.handleUploadPart(ctx, w, r, route.bucket, route.key, q)
		return
	}
	if r.Method == http.MethodPost && q.Has("uploadId") {
		s.handleCompleteMultipartUpload(ctx, w, r, route.bucket, route.key, q.Get("uploadId"))
		return
	}
	if r.Method == http.MethodDelete && q.Has("uploadId") {
		s.handleAbortMultipartUpload(ctx, w, r, route.bucket, route.key, q.Get("uploadId"))
		return
	}
	if r.Method == http.MethodGet && q.Has("uploadId") {
		s.handleListParts(ctx, w, r, route.bucket, route.key, q.Get("uploadId"))
		return
	}

	switch r.Method {
	case http.MethodPut:
		if copySrc := r.Header.Get("x-amz-copy-source"); copySrc != "" {
			s.handleCopyObject(ctx, w, r, route.bucket, route.key, copySrc)
			return
		}
		s.handlePutObject(ctx, w, r, route.bucket, route.key)
	case http.MethodGet:
		s.handleGetObject(ctx, w, r, route.bucket, route.key)
	case http.MethodHead:
		s.handleHeadObject(ctx, w, r, route.bucket, route.key)
	case http.MethodDelete:
		s.handleDeleteObject(ctx, w, r, route.bucket, route.key)
	default:
		writeError(w, r, s3Error{Code: "MethodNotAllowed", Message: "Method not allowed", StatusCode: http.StatusMethodNotAllowed})
	}
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
