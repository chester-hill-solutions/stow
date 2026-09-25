package s3api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/chester-hill-solutions/stow-s3/internal/runthrough"
	"github.com/chester-hill-solutions/stow-s3/internal/version"
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

type outboxPreparedStatsProvider interface {
	OutboxPreparedStats() int
}

type outboxHealthProvider interface {
	OutboxLastError() string
	OutboxRetryAttempts() uint64
}

type outboxEntriesProvider interface {
	OutboxEntries() []runthrough.OutboxEntry
}

type outboxPreparedEntriesProvider interface {
	OutboxPreparedEntries() []runthrough.OutboxEntry
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

func outboxInspectEntries(entries []runthrough.OutboxEntry) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(entries))
	for _, entry := range entries {
		out = append(out, map[string]interface{}{
			"id":            entry.ID,
			"operation":     entry.Operation,
			"bucket":        entry.Bucket,
			"key":           entry.Key,
			"source_bucket": entry.SourceBucket,
			"source_key":    entry.SourceKey,
			"version":       entry.Version,
			"attempts":      entry.Attempts,
			"terminal":      entry.Terminal,
			"prepared":      entry.Prepared,
			"last_error":    outboxErrorClass(entry.LastError),
			"next_attempt":  entry.NextAttempt,
			"claim_owner":   entry.ClaimOwner,
			"claim_until":   entry.ClaimUntil,
		})
	}
	return out
}

func outboxErrorClass(message string) string {
	if message == "" {
		return ""
	}
	message = strings.ToLower(message)
	switch {
	case strings.Contains(message, "not found"):
		return "not_found"
	case strings.Contains(message, "timeout"), strings.Contains(message, "deadline"):
		return "timeout"
	case strings.Contains(message, "accessdenied"), strings.Contains(message, "signature"), strings.Contains(message, "credential"):
		return "authorization"
	case strings.Contains(message, "quota"), strings.Contains(message, "throttl"):
		return "capacity"
	default:
		return "upstream_error"
	}
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
		objects, err := listAllAdminObjects(ctx, s.store, b.Name)
		if err != nil {
			writeAdminError(w, http.StatusInternalServerError, "status is temporarily unavailable")
			return
		}
		objectCount += len(objects)
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
	if provider, ok := s.store.(outboxPreparedStatsProvider); ok {
		payload["outbox_prepared"] = provider.OutboxPreparedStats()
	}
	if provider, ok := s.store.(outboxHealthProvider); ok {
		payload["last_upstream_error"] = outboxErrorClass(provider.OutboxLastError())
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
		objects, err := listAllAdminObjects(ctx, s.store, b.Name)
		if err != nil {
			writeAdminError(w, http.StatusInternalServerError, "inspection is temporarily unavailable")
			return
		}
		count := len(objects)
		uploads, uploadErr := listAllAdminUploads(ctx, s.store, b.Name)
		if uploadErr != nil {
			writeAdminError(w, http.StatusInternalServerError, "inspection is temporarily unavailable")
			return
		}
		multipartUploads += len(uploads)
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
	lastUpstreamError := ""
	var retryAttempts uint64
	if provider, ok := s.store.(outboxHealthProvider); ok {
		lastUpstreamError = outboxErrorClass(provider.OutboxLastError())
		retryAttempts = provider.OutboxRetryAttempts()
	}
	outboxEntries := make([]map[string]interface{}, 0)
	if provider, ok := s.store.(outboxEntriesProvider); ok {
		outboxEntries = outboxInspectEntries(provider.OutboxEntries())
	}
	preparedEntries := make([]map[string]interface{}, 0)
	if provider, ok := s.store.(outboxPreparedEntriesProvider); ok {
		preparedEntries = outboxInspectEntries(provider.OutboxPreparedEntries())
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"buckets":               snaps,
		"multipart_uploads":     multipartUploads,
		"cache_hits":            cacheHits,
		"cache_misses":          cacheMisses,
		"cache_evictions":       cacheEvictions,
		"outbox_pending":        outboxPending,
		"outbox_terminal":       outboxTerminal,
		"outbox_entries":        outboxEntries,
		"outbox_prepared":       preparedEntries,
		"last_upstream_error":   lastUpstreamError,
		"outbox_retry_attempts": retryAttempts,
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
		if errors.Is(err, runthrough.ErrOutboxClaimHeld) {
			writeAdminError(w, http.StatusConflict, "outbox entry is currently claimed")
			return
		}
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
	prepared := 0
	if provider, ok := s.store.(outboxPreparedStatsProvider); ok {
		prepared = provider.OutboxPreparedStats()
	}
	var retryAttempts uint64
	if provider, ok := s.store.(outboxHealthProvider); ok {
		retryAttempts = provider.OutboxRetryAttempts()
	}
	multipartUploads := 0
	buckets, err := s.store.ListBuckets(r.Context())
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, "metrics are temporarily unavailable")
		return
	}
	for _, bucket := range buckets {
		uploads, err := listAllAdminUploads(r.Context(), s.store, bucket.Name)
		if err != nil {
			writeAdminError(w, http.StatusInternalServerError, "metrics are temporarily unavailable")
			return
		}
		multipartUploads += len(uploads)
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = fmt.Fprintf(w, "# HELP stow_cache_hits_total Cache hits observed by the runtime.\n# TYPE stow_cache_hits_total counter\nstow_cache_hits_total %d\n", hits)
	_, _ = fmt.Fprintf(w, "# HELP stow_cache_misses_total Cache misses observed by the runtime.\n# TYPE stow_cache_misses_total counter\nstow_cache_misses_total %d\n", misses)
	_, _ = fmt.Fprintf(w, "# HELP stow_cache_evictions_total Cache entries evicted by policy.\n# TYPE stow_cache_evictions_total counter\nstow_cache_evictions_total %d\n", evictions)
	_, _ = fmt.Fprintf(w, "# HELP stow_multipart_uploads Active multipart uploads.\n# TYPE stow_multipart_uploads gauge\nstow_multipart_uploads %d\n", multipartUploads)
	_, _ = fmt.Fprintf(w, "# HELP stow_outbox_pending_entries Pending outbox entries.\n# TYPE stow_outbox_pending_entries gauge\nstow_outbox_pending_entries %d\n", pending)
	_, _ = fmt.Fprintf(w, "# HELP stow_outbox_terminal_entries Terminal outbox entries.\n# TYPE stow_outbox_terminal_entries gauge\nstow_outbox_terminal_entries %d\n", terminal)
	_, _ = fmt.Fprintf(w, "# HELP stow_outbox_prepared_entries Prepared outbox entries awaiting reconciliation.\n# TYPE stow_outbox_prepared_entries gauge\nstow_outbox_prepared_entries %d\n", prepared)
	_, _ = fmt.Fprintf(w, "# HELP stow_outbox_retry_attempts_total Total upstream propagation attempts.\n# TYPE stow_outbox_retry_attempts_total counter\nstow_outbox_retry_attempts_total %d\n", retryAttempts)
}
