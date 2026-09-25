package main

import (
	"context"
	"fmt"

	"github.com/chester-hill-solutions/stow/internal/runthrough"
	"github.com/chester-hill-solutions/stow/internal/runtime"
	"github.com/chester-hill-solutions/stow/internal/storage"
)

func bindNativeRuntimeStore(store storage.Store, backend string, admin *runthrough.Adapter) (storage.Store, error) {
	options := runtime.Options{
		Backend:    runtime.BackendMemory,
		MaxBytes:   int64(^uint64(0) >> 1),
		MaxObjects: int64(^uint64(0) >> 1),
	}
	if backend == "filesystem" {
		options.Backend = runtime.BackendFilesystem
	}
	instance, err := runtime.OpenWithStore(options, store, nil)
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
