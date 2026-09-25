package runthrough_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/chester-hill-solutions/stow-s3/internal/runthrough"
	"github.com/chester-hill-solutions/stow-s3/internal/storage"
)

func TestAdapter_SeparateCacheEvictsByObjectLimit(t *testing.T) {
	ctx := context.Background()
	local := storage.NewMemoryStore()
	cache := storage.NewMemoryStore()
	_ = local.CreateBucket(ctx, "bucket")
	_ = cache.CreateBucket(ctx, "bucket")
	up := newMockUpstream()
	_ = up.PutObject(ctx, "bucket", "first", bytes.NewReader([]byte("one")), storage.PutOptions{})
	_ = up.PutObject(ctx, "bucket", "second", bytes.NewReader([]byte("two")), storage.PutOptions{})

	adapter := runthrough.NewWithCache(runthrough.Config{
		Policy:     runthrough.PolicyReadThroughCache,
		Revalidate: false,
		Cache:      runthrough.CachePolicy{MaxObjects: 1},
	}, local, cache, up)
	if _, _, err := adapter.GetObject(ctx, "bucket", "first"); err != nil {
		t.Fatalf("cache first: %v", err)
	}
	if _, _, err := adapter.GetObject(ctx, "bucket", "second"); err != nil {
		t.Fatalf("cache second: %v", err)
	}
	if _, err := cache.HeadObject(ctx, "bucket", "first"); !errors.Is(err, storage.ErrObjectNotFound) {
		t.Fatalf("oldest cache object error = %v, want not found", err)
	}
	if _, err := cache.HeadObject(ctx, "bucket", "second"); err != nil {
		t.Fatalf("newest cache object error = %v", err)
	}
	if evictions := adapter.CacheEvictions(); evictions != 1 {
		t.Fatalf("cache evictions = %d, want 1", evictions)
	}
}

func TestAdapter_SeparateCacheEvictsByByteLimit(t *testing.T) {
	ctx := context.Background()
	local := storage.NewMemoryStore()
	cache := storage.NewMemoryStore()
	_ = local.CreateBucket(ctx, "bucket")
	_ = cache.CreateBucket(ctx, "bucket")
	up := newMockUpstream()
	_ = up.PutObject(ctx, "bucket", "large", bytes.NewReader([]byte("12345")), storage.PutOptions{})
	adapter := runthrough.NewWithCache(runthrough.Config{
		Policy:     runthrough.PolicyReadThroughCache,
		Revalidate: false,
		Cache:      runthrough.CachePolicy{MaxBytes: 4},
	}, local, cache, up)
	if _, _, err := adapter.GetObject(ctx, "bucket", "large"); err != nil {
		t.Fatalf("cache large object: %v", err)
	}
	if _, err := cache.HeadObject(ctx, "bucket", "large"); !errors.Is(err, storage.ErrObjectNotFound) {
		t.Fatalf("oversized cache object error = %v, want not found", err)
	}
	if evictions := adapter.CacheEvictions(); evictions != 1 {
		t.Fatalf("cache evictions = %d, want 1", evictions)
	}
}

func TestAdapter_SeparateCacheExpiresEntries(t *testing.T) {
	ctx := context.Background()
	local := storage.NewMemoryStore()
	cache := storage.NewMemoryStore()
	_ = local.CreateBucket(ctx, "bucket")
	_ = cache.CreateBucket(ctx, "bucket")
	up := newMockUpstream()
	if err := up.PutObject(ctx, "bucket", "key", bytes.NewReader([]byte("value")), storage.PutOptions{}); err != nil {
		t.Fatalf("seed upstream: %v", err)
	}
	adapter := runthrough.NewWithCache(runthrough.Config{
		Policy:     runthrough.PolicyReadThroughCache,
		Revalidate: false,
		Cache:      runthrough.CachePolicy{TTL: 5 * time.Millisecond},
	}, local, cache, up)
	if _, _, err := adapter.GetObject(ctx, "bucket", "key"); err != nil {
		t.Fatalf("initial cache fill: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	if _, _, err := adapter.GetObject(ctx, "bucket", "key"); err != nil {
		t.Fatalf("refresh after TTL: %v", err)
	}
	if up.getCalls != 2 {
		t.Fatalf("upstream get calls = %d, want 2", up.getCalls)
	}
	if _, err := cache.HeadObject(ctx, "bucket", "key"); err != nil {
		t.Fatalf("refreshed cache entry: %v", err)
	}
}

func TestAdapter_SeparateCacheDoesNotEvictLocalObjects(t *testing.T) {
	ctx := context.Background()
	local := storage.NewMemoryStore()
	cache := storage.NewMemoryStore()
	_ = local.CreateBucket(ctx, "bucket")
	_ = cache.CreateBucket(ctx, "bucket")
	_, _ = local.PutObject(ctx, "bucket", "local", bytes.NewReader([]byte("local")), storage.PutOptions{})
	_, _ = cache.PutObject(ctx, "bucket", "cached", bytes.NewReader([]byte("cached")), storage.PutOptions{})

	adapter := runthrough.NewWithCache(runthrough.Config{
		Policy:                 runthrough.PolicyReadThroughCache,
		Revalidate:             true,
		EvictOnUpstreamMissing: true,
	}, local, cache, newMockUpstream())

	_, _, err := adapter.GetObject(ctx, "bucket", "cached")
	if err != storage.ErrObjectNotFound {
		t.Fatalf("cached object error = %v, want not found", err)
	}
	if _, err := cache.HeadObject(ctx, "bucket", "cached"); err != storage.ErrObjectNotFound {
		t.Fatalf("cache object survived upstream miss: %v", err)
	}
	if _, err := local.HeadObject(ctx, "bucket", "local"); err != nil {
		t.Fatalf("local object was evicted: %v", err)
	}
}
