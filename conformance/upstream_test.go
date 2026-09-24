package conformance_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"testing"
	"time"

	"github.com/chester-hill-solutions/stow/internal/runthrough"
	"github.com/chester-hill-solutions/stow/internal/storage"
)

// Upstream / run-through conformance tests require a live S3-compatible provider.
// Set STOW_CONFORMANCE_UPSTREAM=1 and configure STOW_* / AWS_* env vars plus
// STOW_LIVE_BUCKET (or STOW_BUCKET).
func TestUpstreamRunThrough(t *testing.T) {
	if os.Getenv("STOW_CONFORMANCE_UPSTREAM") != "1" {
		t.Skip("skipping upstream conformance; set STOW_CONFORMANCE_UPSTREAM=1")
	}
	upstreamConfig := liveUpstreamConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	upstream, adapter := liveAdapter(t, ctx, upstreamConfig)
	key := fmt.Sprintf("stow-conformance/%d", time.Now().UnixNano())
	body := []byte("live run-through conformance")
	defer func() {
		_ = upstream.DeleteObject(context.Background(), upstreamConfig.Bucket, key)
	}()

	meta, err := adapter.PutObject(ctx, upstreamConfig.Bucket, key, bytes.NewReader(body), storage.PutOptions{ContentType: "text/plain"})
	if err != nil {
		t.Fatalf("mirror PutObject: %v", err)
	}
	if meta.ETag == "" {
		t.Fatal("mirror PutObject returned an empty ETag")
	}
	expectation := liveObjectExpectation{bucket: upstreamConfig.Bucket, key: key, body: body, etag: meta.ETag}
	assertLiveUpstreamObject(t, ctx, upstream, expectation)
	assertLiveAdapterObject(t, ctx, adapter, expectation)
}

func liveUpstreamConfig(t *testing.T) runthrough.UpstreamConfig {
	t.Helper()
	config, ok := (runthrough.UpstreamConfig{}).FromEnv()
	if !ok {
		t.Fatal("upstream credentials and endpoint are required")
	}
	if config.Bucket == "" {
		config.Bucket = os.Getenv("STOW_LIVE_BUCKET")
	}
	if config.Bucket == "" {
		t.Fatal("STOW_LIVE_BUCKET or STOW_BUCKET is required")
	}
	return config
}

func liveAdapter(t *testing.T, ctx context.Context, config runthrough.UpstreamConfig) (*runthrough.S3Client, *runthrough.Adapter) {
	t.Helper()
	upstream, err := runthrough.NewS3Client(config)
	if err != nil {
		t.Fatalf("new upstream client: %v", err)
	}
	local := storage.NewMemoryStore()
	cache := storage.NewMemoryStore()
	if err := local.CreateBucket(ctx, config.Bucket); err != nil {
		t.Fatalf("create local bucket: %v", err)
	}
	adapter := runthrough.NewWithOutbox(runthrough.Config{
		Policy:          runthrough.PolicyMirrorWrites,
		AllowLiveWrites: true,
		Upstream:        config,
	}, local, cache, upstream, runthrough.NewMemoryOutbox())
	t.Cleanup(func() {
		if err := adapter.Close(); err != nil {
			t.Logf("adapter close: %v", err)
		}
	})
	return upstream, adapter
}

type liveObjectExpectation struct {
	bucket string
	key    string
	body   []byte
	etag   string
}

func assertLiveUpstreamObject(t *testing.T, ctx context.Context, upstream *runthrough.S3Client, expected liveObjectExpectation) {
	t.Helper()
	reader, meta, err := upstream.GetObject(ctx, expected.bucket, expected.key)
	if err != nil {
		t.Fatalf("upstream GetObject: %v", err)
	}
	defer reader.Close()
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read upstream body: %v", err)
	}
	if !bytes.Equal(body, expected.body) {
		t.Fatalf("upstream body = %q, want %q", body, expected.body)
	}
	if meta.ContentType != "text/plain" {
		t.Fatalf("upstream content type = %q", meta.ContentType)
	}
}

func assertLiveAdapterObject(t *testing.T, ctx context.Context, adapter *runthrough.Adapter, expected liveObjectExpectation) {
	t.Helper()
	reader, meta, err := adapter.GetObject(ctx, expected.bucket, expected.key)
	if err != nil {
		t.Fatalf("adapter GetObject: %v", err)
	}
	body, err := io.ReadAll(reader)
	reader.Close()
	if err != nil {
		t.Fatalf("read adapter body: %v", err)
	}
	if !bytes.Equal(body, expected.body) || meta.ETag != expected.etag {
		t.Fatalf("adapter object did not retain mirrored record: body=%q etag=%q", body, meta.ETag)
	}
}
