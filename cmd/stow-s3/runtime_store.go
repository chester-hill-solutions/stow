package main

import (
	"context"
	"fmt"

	"github.com/chester-hill-solutions/stow-s3/internal/runthrough"
	"github.com/chester-hill-solutions/stow-s3/internal/runtime"
	"github.com/chester-hill-solutions/stow-s3/internal/storage"
)

// nativeStorageLimits bounds what one local Stow process will hold. The
// runtime enforces these for every native S3 request, so the values only have
// to be supplied here. A non-positive value means unlimited, which preserves
// the existing unbounded behavior of a long-lived server; a scoped session
// passes explicit limits.
type nativeStorageLimits struct {
	maxBytes   int64
	maxObjects int64
}

func (l nativeStorageLimits) bytes() int64 {
	if l.maxBytes <= 0 {
		return runtime.UnlimitedBytes
	}
	return l.maxBytes
}

func (l nativeStorageLimits) objects() int64 {
	if l.maxObjects <= 0 {
		return runtime.UnlimitedObjects
	}
	return l.maxObjects
}

func bindNativeRuntimeStore(store storage.Store, backend runtime.Backend, admin *runthrough.Adapter, limits nativeStorageLimits) (storage.Store, error) {
	instance, err := runtime.OpenWithStore(runtime.Options{
		Backend:    backend,
		MaxBytes:   limits.bytes(),
		MaxObjects: limits.objects(),
	}, store, nil)
	if err != nil {
		return nil, fmt.Errorf("open native runtime: %w", err)
	}
	adapter, err := runtime.NewStoreAdapter(instance)
	if err != nil {
		_ = instance.Close()
		return nil, fmt.Errorf("create native runtime adapter: %w", err)
	}
	if admin != nil {
		return &runThroughRuntimeStore{StoreAdapter: adapter, admin: admin}, nil
	}
	return adapter, nil
}

type runThroughRuntimeStore struct {
	*runtime.StoreAdapter
	admin *runthrough.Adapter
}

func (s *runThroughRuntimeStore) Close() error { return s.StoreAdapter.Close() }

func (s *runThroughRuntimeStore) CacheStats() (uint64, uint64) {
	return s.admin.CacheStats()
}

func (s *runThroughRuntimeStore) CacheEvictions() uint64 { return s.admin.CacheEvictions() }

func (s *runThroughRuntimeStore) OutboxStats() (int, int) { return s.admin.OutboxStats() }

func (s *runThroughRuntimeStore) OutboxPreparedStats() int { return s.admin.OutboxPreparedStats() }

func (s *runThroughRuntimeStore) OutboxLastError() string { return s.admin.OutboxLastError() }

func (s *runThroughRuntimeStore) OutboxRetryAttempts() uint64 { return s.admin.OutboxRetryAttempts() }

func (s *runThroughRuntimeStore) OutboxEntries() []runthrough.OutboxEntry {
	return s.admin.OutboxEntries()
}

func (s *runThroughRuntimeStore) OutboxPreparedEntries() []runthrough.OutboxEntry {
	return s.admin.OutboxPreparedEntries()
}

func (s *runThroughRuntimeStore) RetryPending(ctx context.Context) error {
	return s.admin.RetryPending(ctx)
}

func (s *runThroughRuntimeStore) DiscardOutboxEntry(id string) error {
	return s.admin.DiscardOutboxEntry(id)
}

var _ storage.Store = (*runThroughRuntimeStore)(nil)
