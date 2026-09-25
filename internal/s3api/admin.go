package s3api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/chester-hill-solutions/stow/internal/storage"
	"github.com/chester-hill-solutions/stow/internal/version"
)

type cacheStatsProvider interface {
	CacheStats() (hits, misses uint64)
}

type cacheEvictionsProvider interface {
	CacheEvictions() uint64
}

type outboxStatsProvider interface {
	OutboxStats() (pending, terminal int)
}

type outboxAdminProvider interface {
	RetryPending(context.Context) error
	DiscardOutboxEntry(string) error
}

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
	case "/_stow/metrics":
		if r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		s.writeMetrics(w, r)
	case "/_stow/outbox/retry":
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		s.retryOutbox(w, r)
	case "/_stow/outbox/discard":
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		s.discardOutbox(w, r)
	default:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
	}
}

func writeAdminError(w http.ResponseWriter, status int, message string) {
	http.Error(w, message, status)
}

func (s *Server) writeStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	buckets, err := s.store.ListBuckets(ctx)
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, "status is temporarily unavailable")
		return
	}
	objectCount := 0
	for _, b := range buckets {
		list, err := s.store.ListObjectsV2(ctx, b.Name, storage.ListOptions{MaxKeys: 10000})
		if err != nil {
			writeAdminError(w, http.StatusInternalServerError, "status is temporarily unavailable")
			return
		}
		objectCount += len(list.Objects)
	}
	addr := s.Addr()
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
	if provider, ok := s.store.(outboxStatsProvider); ok {
		pending, terminal := provider.OutboxStats()
		payload["outbox_pending"] = pending
		payload["outbox_terminal"] = terminal
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

func (s *Server) writeInspect(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	bucketFilter := r.URL.Query().Get("bucket")
	buckets, err := s.store.ListBuckets(ctx)
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, "inspection is temporarily unavailable")
		return
	}

	type bucketSnap struct {
		Name         string `json:"name"`
		ObjectCount  int    `json:"object_count"`
		CreationDate string `json:"creation_date"`
	}
	var snaps []bucketSnap
	multipartUploads := 0
	for _, b := range buckets {
		if bucketFilter != "" && b.Name != bucketFilter {
			continue
		}
		list, err := s.store.ListObjectsV2(ctx, b.Name, storage.ListOptions{MaxKeys: 10000})
		if err != nil {
			writeAdminError(w, http.StatusInternalServerError, "inspection is temporarily unavailable")
			return
		}
		count := len(list.Objects)
		uploads, uploadErr := s.store.ListMultipartUploads(ctx, b.Name, storage.MultipartListOptions{MaxUploads: 10000})
		if uploadErr != nil {
			writeAdminError(w, http.StatusInternalServerError, "inspection is temporarily unavailable")
			return
		}
		multipartUploads += len(uploads.Uploads)
		snaps = append(snaps, bucketSnap{
			Name:         b.Name,
			ObjectCount:  count,
			CreationDate: formatTime(b.CreationDate),
		})
	}
	var cacheHits, cacheMisses uint64
	if provider, ok := s.store.(cacheStatsProvider); ok {
		cacheHits, cacheMisses = provider.CacheStats()
	}
	var cacheEvictions uint64
	if provider, ok := s.store.(cacheEvictionsProvider); ok {
		cacheEvictions = provider.CacheEvictions()
	}
	outboxPending, outboxTerminal := 0, 0
	if provider, ok := s.store.(outboxStatsProvider); ok {
		outboxPending, outboxTerminal = provider.OutboxStats()
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"buckets":           snaps,
		"multipart_uploads": multipartUploads,
		"cache_hits":        cacheHits,
		"cache_misses":      cacheMisses,
		"cache_evictions":   cacheEvictions,
		"outbox_pending":    outboxPending,
		"outbox_terminal":   outboxTerminal,
	})
}

func (s *Server) retryOutbox(w http.ResponseWriter, r *http.Request) {
	provider, ok := s.store.(outboxAdminProvider)
	if !ok {
		http.Error(w, "outbox administration is unavailable", http.StatusNotImplemented)
		return
	}
	if err := provider.RetryPending(r.Context()); err != nil {
		writeAdminError(w, http.StatusInternalServerError, "outbox retry failed")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) discardOutbox(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}
	provider, ok := s.store.(outboxAdminProvider)
	if !ok {
		http.Error(w, "outbox administration is unavailable", http.StatusNotImplemented)
		return
	}
	if err := provider.DiscardOutboxEntry(id); err != nil {
		writeAdminError(w, http.StatusNotFound, "outbox entry was not found")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) writeMetrics(w http.ResponseWriter, r *http.Request) {
	var hits, misses uint64
	if provider, ok := s.store.(cacheStatsProvider); ok {
		hits, misses = provider.CacheStats()
	}
	var evictions uint64
	if provider, ok := s.store.(cacheEvictionsProvider); ok {
		evictions = provider.CacheEvictions()
	}
	pending, terminal := 0, 0
	if provider, ok := s.store.(outboxStatsProvider); ok {
		pending, terminal = provider.OutboxStats()
	}
	multipartUploads := 0
	buckets, err := s.store.ListBuckets(r.Context())
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, "metrics are temporarily unavailable")
		return
	}
	for _, bucket := range buckets {
		uploads, err := s.store.ListMultipartUploads(r.Context(), bucket.Name, storage.MultipartListOptions{MaxUploads: 10000})
		if err != nil {
			writeAdminError(w, http.StatusInternalServerError, "metrics are temporarily unavailable")
			return
		}
		multipartUploads += len(uploads.Uploads)
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = fmt.Fprintf(w, "# HELP stow_cache_hits_total Cache hits observed by the runtime.\n# TYPE stow_cache_hits_total counter\nstow_cache_hits_total %d\n", hits)
	_, _ = fmt.Fprintf(w, "# HELP stow_cache_misses_total Cache misses observed by the runtime.\n# TYPE stow_cache_misses_total counter\nstow_cache_misses_total %d\n", misses)
	_, _ = fmt.Fprintf(w, "# HELP stow_cache_evictions_total Cache entries evicted by policy.\n# TYPE stow_cache_evictions_total counter\nstow_cache_evictions_total %d\n", evictions)
	_, _ = fmt.Fprintf(w, "# HELP stow_multipart_uploads Active multipart uploads.\n# TYPE stow_multipart_uploads gauge\nstow_multipart_uploads %d\n", multipartUploads)
	_, _ = fmt.Fprintf(w, "# HELP stow_outbox_pending_entries Pending outbox entries.\n# TYPE stow_outbox_pending_entries gauge\nstow_outbox_pending_entries %d\n", pending)
	_, _ = fmt.Fprintf(w, "# HELP stow_outbox_terminal_entries Terminal outbox entries.\n# TYPE stow_outbox_terminal_entries gauge\nstow_outbox_terminal_entries %d\n", terminal)
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
