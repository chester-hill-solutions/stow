package runthrough_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/chester-hill-solutions/stow/internal/runthrough"
	"github.com/chester-hill-solutions/stow/internal/storage"
)

func TestListObjectsUsesUpstreamOnlyWithLocalPrecedence(t *testing.T) {
	ctx := context.Background()
	local := storage.NewMemoryStore()
	cache := storage.NewMemoryStore()
	if err := local.CreateBucket(ctx, "bucket"); err != nil {
		t.Fatalf("create local bucket: %v", err)
	}
	if err := cache.CreateBucket(ctx, "bucket"); err != nil {
		t.Fatalf("create cache bucket: %v", err)
	}
	localMeta, err := local.PutObject(ctx, "bucket", "shared", bytes.NewReader([]byte("local")), storage.PutOptions{})
	if err != nil {
		t.Fatalf("put local: %v", err)
	}
	if _, err := cache.PutObject(ctx, "bucket", "shared", bytes.NewReader([]byte("cache")), storage.PutOptions{}); err != nil {
		t.Fatalf("put cached shared: %v", err)
	}
	if _, err := cache.PutObject(ctx, "bucket", "stale", bytes.NewReader([]byte("stale")), storage.PutOptions{}); err != nil {
		t.Fatalf("put stale cache: %v", err)
	}

	up := newMockUpstream()
	up.objects[objectKey("bucket", "shared")] = storage.ObjectMeta{
		Bucket: "bucket",
		Key:    "shared",
		Size:   99,
		ETag:   "upstream",
	}
	adapter := runthrough.NewWithCache(runthrough.Config{
		Policy:     runthrough.PolicyReadThroughCache,
		Revalidate: false,
	}, local, cache, up)

	result, err := adapter.ListObjectsV2(ctx, "bucket", storage.ListOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(result.Objects) != 1 || result.Objects[0].Size != localMeta.Size {
		t.Fatalf("successful list = %+v, want local metadata only", result.Objects)
	}

	up.listErr = errors.New("upstream unavailable")
	result, err = adapter.ListObjectsV2(ctx, "bucket", storage.ListOptions{})
	if err != nil {
		t.Fatalf("fallback list: %v", err)
	}
	if len(result.Objects) != 2 {
		t.Fatalf("fallback objects = %+v, want local plus cache", result.Objects)
	}
}
