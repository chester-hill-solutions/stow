package metadata_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chester-hill-solutions/stow/internal/metadata"
	"github.com/chester-hill-solutions/stow/internal/storage"
)

func TestSQLiteSyncFromStore(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore()
	_ = store.CreateBucket(ctx, "logs")
	_, _ = store.PutObject(ctx, "logs", "2026/04.log", strings.NewReader("entry"), storage.PutOptions{
		ContentType: "text/plain",
	})

	dbPath := filepath.Join(t.TempDir(), "meta.db")
	db, err := metadata.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	if err := db.SyncFromStore(ctx, store); err != nil {
		t.Fatalf("sync: %v", err)
	}

	meta, err := db.GetObject(ctx, "logs", "2026/04.log")
	if err != nil {
		t.Fatalf("get object: %v", err)
	}
	if meta.Size != 5 || meta.ContentType != "text/plain" {
		t.Fatalf("meta = %+v", meta)
	}

	buckets, err := db.ListBuckets(ctx)
	if err != nil {
		t.Fatalf("list buckets: %v", err)
	}
	if len(buckets) != 1 || buckets[0].Name != "logs" {
		t.Fatalf("buckets = %+v", buckets)
	}
}

func TestSQLiteMultipartIndex(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "meta.db")
	db, err := metadata.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	_ = db.UpsertBucket(ctx, storage.BucketInfo{Name: "data"})
	upload := storage.MultipartUpload{
		UploadID:  "abc123",
		Bucket:    "data",
		Key:       "file",
		Initiated: storage.BucketInfo{}.CreationDate,
	}
	if err := db.CreateMultipartUpload(ctx, upload); err != nil {
		t.Fatalf("create upload: %v", err)
	}
	part := storage.PartInfo{PartNumber: 1, ETag: "\"etag\"", Size: 3}
	if err := db.UpsertPart(ctx, upload.UploadID, part); err != nil {
		t.Fatalf("upsert part: %v", err)
	}
	parts, err := db.ListParts(ctx, upload.UploadID)
	if err != nil || len(parts) != 1 || parts[0].PartNumber != 1 {
		t.Fatalf("parts = %+v err=%v", parts, err)
	}
}
