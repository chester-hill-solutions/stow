package runthrough_test

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"sync"
	"testing"

	"github.com/chester-hill-solutions/stow/internal/runthrough"
	"github.com/chester-hill-solutions/stow/internal/storage"
)

type mockUpstream struct {
	mu sync.Mutex

	headCalls int
	getCalls  int
	putCalls  int
	delCalls  int

	objects map[string]storage.ObjectMeta
	bodies  map[string][]byte
}

func newMockUpstream() *mockUpstream {
	return &mockUpstream{
		objects: make(map[string]storage.ObjectMeta),
		bodies:  make(map[string][]byte),
	}
}

func objectKey(bucket, key string) string {
	return bucket + "/" + key
}

func (m *mockUpstream) HeadObject(_ context.Context, bucket, key string) (*storage.ObjectMeta, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.headCalls++
	meta, ok := m.objects[objectKey(bucket, key)]
	if !ok {
		return nil, storage.ErrObjectNotFound
	}
	out := meta
	return &out, nil
}

func (m *mockUpstream) GetObject(_ context.Context, bucket, key string) (io.ReadCloser, *storage.ObjectMeta, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.getCalls++
	k := objectKey(bucket, key)
	meta, ok := m.objects[k]
	if !ok {
		return nil, nil, storage.ErrObjectNotFound
	}
	data := m.bodies[k]
	out := meta
	return io.NopCloser(bytes.NewReader(data)), &out, nil
}

func (m *mockUpstream) PutObject(_ context.Context, bucket, key string, body io.Reader, opts storage.PutOptions) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.putCalls++
	data, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	k := objectKey(bucket, key)
	etag, _, err := storagePutMeta(data)
	if err != nil {
		return err
	}
	m.bodies[k] = data
	m.objects[k] = storage.ObjectMeta{
		Bucket:      bucket,
		Key:         key,
		Size:        int64(len(data)),
		ETag:        etag,
		ContentType: opts.ContentType,
		Metadata:    opts.Metadata,
	}
	return nil
}

func (m *mockUpstream) DeleteObject(_ context.Context, bucket, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.delCalls++
	delete(m.objects, objectKey(bucket, key))
	delete(m.bodies, objectKey(bucket, key))
	return nil
}

func (m *mockUpstream) ListObjectsV2(_ context.Context, bucket string, opts storage.ListOptions) (*storage.ListResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var objects []storage.ObjectMeta
	for k, meta := range m.objects {
		if meta.Bucket != bucket {
			continue
		}
		if opts.Prefix != "" && !bytesHasPrefix(meta.Key, opts.Prefix) {
			continue
		}
		objects = append(objects, meta)
		_ = k
	}
	return &storage.ListResult{Objects: objects, KeyCount: len(objects)}, nil
}

func storagePutMeta(data []byte) (string, []byte, error) {
	return storageETag(data), data, nil
}

func storageETag(data []byte) string {
	sum := md5.Sum(data)
	return fmt.Sprintf("\"%s\"", hex.EncodeToString(sum[:]))
}

func bytesHasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func TestAdapter_WriteGuardBlocksUpstreamPut(t *testing.T) {
	local := storage.NewMemoryStore()
	ctx := context.Background()
	_ = local.CreateBucket(ctx, "b")

	up := newMockUpstream()
	cfg := runthrough.Config{
		Policy:          runthrough.PolicyReadThroughCache,
		AllowLiveWrites: false,
		Revalidate:      false,
	}
	adapter := runthrough.New(cfg, local, up)

	err := adapter.PropagateUpstreamPut(ctx, "b", "k", bytes.NewReader([]byte("x")), storage.PutOptions{})
	if err != runthrough.ErrLiveWritesDisabled {
		t.Fatalf("PropagateUpstreamPut() error = %v, want ErrLiveWritesDisabled", err)
	}
	if up.putCalls != 0 {
		t.Fatalf("upstream put calls = %d, want 0", up.putCalls)
	}
}

func TestAdapter_PutObjectLocalOnlyWithoutLiveWrites(t *testing.T) {
	local := storage.NewMemoryStore()
	ctx := context.Background()
	_ = local.CreateBucket(ctx, "b")

	up := newMockUpstream()
	cfg := runthrough.Config{
		Policy:          runthrough.PolicyReadThroughCache,
		AllowLiveWrites: false,
		Revalidate:      false,
	}
	adapter := runthrough.New(cfg, local, up)

	_, err := adapter.PutObject(ctx, "b", "k", bytes.NewReader([]byte("hello")), storage.PutOptions{})
	if err != nil {
		t.Fatalf("PutObject() error = %v", err)
	}
	if up.putCalls != 0 {
		t.Fatalf("upstream put calls = %d, want 0", up.putCalls)
	}
	rc, meta, err := local.GetObject(ctx, "b", "k")
	if err != nil {
		t.Fatalf("local GetObject() error = %v", err)
	}
	defer rc.Close()
	data, _ := io.ReadAll(rc)
	if string(data) != "hello" {
		t.Fatalf("local data = %q", data)
	}
	if meta == nil || meta.Size != 5 {
		t.Fatalf("unexpected meta: %+v", meta)
	}
}

func TestAdapter_PutObjectDualWriteWithLiveWrites(t *testing.T) {
	local := storage.NewMemoryStore()
	ctx := context.Background()
	_ = local.CreateBucket(ctx, "b")

	up := newMockUpstream()
	cfg := runthrough.Config{
		Policy:          runthrough.PolicyReadThroughCache,
		AllowLiveWrites: true,
		Revalidate:      false,
	}
	adapter := runthrough.New(cfg, local, up)

	_, err := adapter.PutObject(ctx, "b", "k", bytes.NewReader([]byte("live")), storage.PutOptions{ContentType: "text/plain"})
	if err != nil {
		t.Fatalf("PutObject() error = %v", err)
	}
	if up.putCalls != 1 {
		t.Fatalf("upstream put calls = %d, want 1", up.putCalls)
	}
	rc, _, err := up.GetObject(ctx, "b", "k")
	if err != nil {
		t.Fatalf("upstream GetObject() error = %v", err)
	}
	defer rc.Close()
	data, _ := io.ReadAll(rc)
	if string(data) != "live" {
		t.Fatalf("upstream data = %q", data)
	}
}

func TestAdapter_ReadThroughCacheMiss(t *testing.T) {
	local := storage.NewMemoryStore()
	ctx := context.Background()
	_ = local.CreateBucket(ctx, "b")

	up := newMockUpstream()
	_ = up.PutObject(ctx, "b", "k", bytes.NewReader([]byte("upstream-bytes")), storage.PutOptions{})

	cfg := runthrough.Config{
		Policy:     runthrough.PolicyReadThroughCache,
		Revalidate: false,
	}
	adapter := runthrough.New(cfg, local, up)

	rc, meta, err := adapter.GetObject(ctx, "b", "k")
	if err != nil {
		t.Fatalf("GetObject() error = %v", err)
	}
	defer rc.Close()
	data, _ := io.ReadAll(rc)
	if string(data) != "upstream-bytes" {
		t.Fatalf("data = %q", data)
	}
	if meta == nil {
		t.Fatal("expected meta")
	}
	if up.getCalls < 1 {
		t.Fatalf("expected upstream get, got %d", up.getCalls)
	}

	_, err = local.HeadObject(ctx, "b", "k")
	if err != nil {
		t.Fatalf("expected local cache populated, got %v", err)
	}
}

func TestAdapter_RevalidationSkipsUpstreamWhenDisabled(t *testing.T) {
	local := storage.NewMemoryStore()
	ctx := context.Background()
	_ = local.CreateBucket(ctx, "b")
	_, _ = local.PutObject(ctx, "b", "k", bytes.NewReader([]byte("local")), storage.PutOptions{})

	up := newMockUpstream()
	_ = up.PutObject(ctx, "b", "k", bytes.NewReader([]byte("newer-upstream")), storage.PutOptions{})

	cfg := runthrough.Config{
		Policy:     runthrough.PolicyReadThroughCache,
		Revalidate: false,
	}
	adapter := runthrough.New(cfg, local, up)

	rc, _, err := adapter.GetObject(ctx, "b", "k")
	if err != nil {
		t.Fatalf("GetObject() error = %v", err)
	}
	defer rc.Close()
	data, _ := io.ReadAll(rc)
	if string(data) != "local" {
		t.Fatalf("data = %q, want cached local without revalidation", data)
	}
	if up.headCalls != 0 {
		t.Fatalf("upstream head calls = %d, want 0 when revalidation disabled", up.headCalls)
	}
}

func TestAdapter_BucketFilter(t *testing.T) {
	local := storage.NewMemoryStore()
	ctx := context.Background()
	_ = local.CreateBucket(ctx, "allowed")
	_ = local.CreateBucket(ctx, "blocked")

	up := newMockUpstream()
	_ = up.PutObject(ctx, "allowed", "k", bytes.NewReader([]byte("yes")), storage.PutOptions{})
	_ = up.PutObject(ctx, "blocked", "k", bytes.NewReader([]byte("no")), storage.PutOptions{})

	cfg := runthrough.Config{
		Policy:     runthrough.PolicyReadThroughCache,
		Revalidate: false,
		Upstream:   runthrough.UpstreamConfig{Bucket: "allowed"},
	}
	adapter := runthrough.New(cfg, local, up)

	rc, _, err := adapter.GetObject(ctx, "allowed", "k")
	if err != nil {
		t.Fatalf("allowed bucket GetObject() error = %v", err)
	}
	rc.Close()

	_, _, err = adapter.GetObject(ctx, "blocked", "k")
	if err != storage.ErrObjectNotFound {
		t.Fatalf("blocked bucket error = %v, want ErrObjectNotFound", err)
	}
	if up.getCalls != 1 {
		t.Fatalf("upstream get calls = %d, want 1 (filtered bucket only)", up.getCalls)
	}
}
