package conformance_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chester-hill-solutions/stow-s3/internal/runthrough"
	"github.com/chester-hill-solutions/stow-s3/internal/storage"
)

// Upstream / run-through conformance tests require a live S3-compatible provider.
// Set STOW_CONFORMANCE_UPSTREAM=1 and STOW_CONFORMANCE_DISPOSABLE=1, identify
// the provider with STOW_CONFORMANCE_PROVIDER, and configure STOW_* credentials
// plus STOW_LIVE_BUCKET and STOW_LIVE_BUCKET_PREFIX.
func TestUpstreamRunThrough(t *testing.T) {
	if !liveEnvEnabled("STOW_CONFORMANCE_UPSTREAM") {
		t.Skip("skipping upstream conformance; set STOW_CONFORMANCE_UPSTREAM=1")
	}
	if liveEnvEnabled("STOW_CONFORMANCE_DRY_RUN") {
		t.Skip("live conformance dry-run; no provider request was made")
	}
	if !liveEnvEnabled("STOW_CONFORMANCE_DISPOSABLE") {
		t.Fatal("STOW_CONFORMANCE_DISPOSABLE=1 is required for live conformance")
	}

	provider, err := normalizeLiveProvider(os.Getenv("STOW_CONFORMANCE_PROVIDER"))
	if err != nil {
		t.Fatal(err)
	}
	prefix, err := normalizeLivePrefix(os.Getenv("STOW_LIVE_BUCKET_PREFIX"))
	if err != nil {
		t.Fatal(err)
	}
	upstreamConfig := liveUpstreamConfig(t)
	runID := normalizeLiveRunID(os.Getenv("STOW_CONFORMANCE_RUN_ID"))
	key := liveObjectKey(prefix, provider, runID, time.Now().UnixNano())
	body := []byte(fmt.Sprintf("live run-through conformance provider=%s run=%s", provider, runID))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	upstream, adapter := liveAdapter(t, ctx, upstreamConfig)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		if err := removeAndVerifyLiveObject(cleanupCtx, upstream, upstreamConfig.Bucket, key); err != nil {
			t.Errorf("live cleanup verification: %v", err)
		}
	})
	t.Logf("live provider=%s bucket=%s key=%s", provider, upstreamConfig.Bucket, key)

	meta, err := adapter.PutObject(ctx, upstreamConfig.Bucket, key, bytes.NewReader(body), storage.PutOptions{ContentType: "text/plain"})
	if err != nil {
		t.Fatalf("mirror PutObject: %v", err)
	}
	if meta.ETag == "" {
		t.Fatal("mirror PutObject returned an empty ETag")
	}
	if pending, terminal := adapter.OutboxStats(); pending != 0 || terminal != 0 {
		t.Fatalf("outbox after successful mirror: pending=%d terminal=%d", pending, terminal)
	}
	expectation := liveObjectExpectation{bucket: upstreamConfig.Bucket, key: key, body: body, etag: meta.ETag}
	assertLiveObject(t, ctx, upstream, expectation)
	assertLiveObject(t, ctx, adapter, expectation)
}

func liveEnvEnabled(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func normalizeLiveProvider(raw string) (string, error) {
	switch provider := strings.ToLower(strings.TrimSpace(raw)); provider {
	case "aws-s3", "cloudflare-r2", "custom":
		return provider, nil
	case "":
		return "", errors.New("STOW_CONFORMANCE_PROVIDER is required for live conformance")
	default:
		return "", fmt.Errorf("unsupported STOW_CONFORMANCE_PROVIDER %q", raw)
	}
}

func normalizeLivePrefix(raw string) (string, error) {
	prefix := strings.TrimSpace(raw)
	if prefix == "" {
		return "", errors.New("STOW_LIVE_BUCKET_PREFIX is required for live conformance")
	}
	if strings.HasPrefix(prefix, "/") {
		return "", errors.New("STOW_LIVE_BUCKET_PREFIX must be relative to the bucket")
	}
	if strings.Contains(prefix, `\`) {
		return "", errors.New("STOW_LIVE_BUCKET_PREFIX must use S3 forward slashes")
	}
	for _, char := range prefix {
		if char < 0x20 || char == 0x7f {
			return "", errors.New("STOW_LIVE_BUCKET_PREFIX must not contain control characters")
		}
	}
	prefix = strings.Trim(prefix, "/")
	for _, segment := range strings.Split(prefix, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", errors.New("STOW_LIVE_BUCKET_PREFIX must contain canonical, non-traversing path segments")
		}
	}
	return prefix, nil
}

func normalizeLiveRunID(raw string) string {
	var normalized strings.Builder
	for _, char := range strings.TrimSpace(raw) {
		switch {
		case char >= 'a' && char <= 'z',
			char >= 'A' && char <= 'Z',
			char >= '0' && char <= '9',
			char == '-', char == '_', char == '.':
			normalized.WriteRune(char)
		default:
			normalized.WriteByte('-')
		}
	}
	if normalized.Len() == 0 {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return normalized.String()
}

func liveObjectKey(prefix, provider, runID string, nonce int64) string {
	return fmt.Sprintf("%s/%s/%s/%d", prefix, provider, runID, nonce)
}

func liveUpstreamConfig(t *testing.T) runthrough.UpstreamConfig {
	t.Helper()
	config, ok := (runthrough.UpstreamConfig{}).FromEnv()
	if !ok {
		t.Fatal("upstream endpoint, access key, and secret key are required")
	}
	config.Bucket = strings.TrimSpace(os.Getenv("STOW_LIVE_BUCKET"))
	if config.Bucket == "" {
		t.Fatal("STOW_LIVE_BUCKET is required")
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
	outbox, err := runthrough.NewFileOutbox(filepath.Join(t.TempDir(), "outbox.json"))
	if err != nil {
		t.Fatalf("new file outbox: %v", err)
	}
	adapter := runthrough.NewWithOutbox(runthrough.Config{
		Policy:          runthrough.PolicyMirrorWrites,
		AllowLiveWrites: true,
		Upstream:        config,
	}, local, cache, upstream, outbox)
	t.Cleanup(func() {
		if err := adapter.Close(); err != nil {
			t.Logf("adapter close: %v", err)
		}
	})
	return upstream, adapter
}

type liveObjectCleaner interface {
	DeleteObject(context.Context, string, string) error
	HeadObject(context.Context, string, string) (*storage.ObjectMeta, error)
	ListObjectsV2(context.Context, string, storage.ListOptions) (*storage.ListResult, error)
}

func removeAndVerifyLiveObject(ctx context.Context, upstream liveObjectCleaner, bucket, key string) error {
	if err := upstream.DeleteObject(ctx, bucket, key); err != nil && !errors.Is(err, storage.ErrObjectNotFound) {
		return fmt.Errorf("delete %q: %w", key, err)
	}
	for {
		_, err := upstream.HeadObject(ctx, bucket, key)
		if errors.Is(err, storage.ErrObjectNotFound) {
			break
		}
		if err != nil {
			return fmt.Errorf("head %q after delete: %w", key, err)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("head %q still exists after delete: %w", key, ctx.Err())
		case <-time.After(200 * time.Millisecond):
		}
	}

	listed, err := upstream.ListObjectsV2(ctx, bucket, storage.ListOptions{Prefix: key})
	if err != nil {
		return fmt.Errorf("list cleanup prefix %q: %w", key, err)
	}
	for _, object := range listed.Objects {
		if object.Key == key {
			return fmt.Errorf("cleanup listing still contains %q", key)
		}
	}
	return nil
}

// liveObjectSource is the one shape both the raw provider client and the
// run-through adapter expose, so a single assertion covers the whole mirror
// path instead of one near-identical helper per side.
type liveObjectSource interface {
	GetObject(context.Context, string, string) (io.ReadCloser, *storage.ObjectMeta, error)
}

type liveObjectExpectation struct {
	bucket string
	key    string
	body   []byte
	etag   string
}

func assertLiveObject(t *testing.T, ctx context.Context, source liveObjectSource, expected liveObjectExpectation) {
	t.Helper()
	reader, meta, err := source.GetObject(ctx, expected.bucket, expected.key)
	if err != nil {
		t.Fatalf("GetObject: %v", err)
	}
	defer reader.Close()
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !bytes.Equal(body, expected.body) || meta.ETag != expected.etag {
		t.Fatalf("object lost the mirrored record: body=%q etag=%q, want body=%q etag=%q",
			body, meta.ETag, expected.body, expected.etag)
	}
	if meta.ContentType != "text/plain" {
		t.Fatalf("content type = %q, want text/plain", meta.ContentType)
	}
}
