package fs_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/chester-hill-solutions/stow-s3/internal/storage"
	"github.com/chester-hill-solutions/stow-s3/internal/storage/fs"
)

// nameMax is the portable limit on one filesystem path component. A key is hex
// encoded, so the key length that overflows a single component is half of this.
const nameMax = 255

// TestFilesystemStoreLongKeysRoundTrip covers the sizes the issue named, because
// the boundary is the whole bug: 127 bytes encodes to 254 characters and fits,
// 128 encodes to 256 and did not. Before the fix every size above 127 failed with
// ENAMETOOLONG even though ValidateKey accepts up to 1024, so the caller got a 500
// for a key it was entitled to send.
func TestFilesystemStoreLongKeysRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := newContractStore(t)
	if err := store.CreateBucket(ctx, "longkeys"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}

	for _, size := range []int{1, 127, 128, 200, 512, 1024} {
		key := strings.Repeat("k", size)
		body := "body-" + key
		if _, err := store.PutObject(ctx, "longkeys", key, strings.NewReader(body), storage.PutOptions{}); err != nil {
			t.Fatalf("put %d-byte key: %v", size, err)
		}

		rc, meta, err := store.GetObject(ctx, "longkeys", key)
		if err != nil {
			t.Fatalf("get %d-byte key: %v", size, err)
		}
		got, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatalf("read %d-byte key body: %v", size, err)
		}
		if string(got) != body {
			t.Fatalf("%d-byte key body = %d bytes, want %d", size, len(got), len(body))
		}
		if meta.Key != key {
			t.Fatalf("%d-byte key head key = %q, want the original", size, meta.Key)
		}
		if meta.Size != int64(len(body)) {
			t.Fatalf("%d-byte key size = %d, want %d", size, meta.Size, len(body))
		}
	}
}

// TestFilesystemStoreShortKeysStayFlat guards the migration claim. Only keys
// that did not fit were ever sharded, and they were never successfully written,
// so every already-stored object sits directly in the objects directory. If a
// short key ever started nesting, existing data directories would stop resolving.
func TestFilesystemStoreShortKeysStayFlat(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := fs.NewFilesystemStore(dir)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.CreateBucket(ctx, "flat"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}

	// 127 bytes is the largest key that fits a single component unencoded.
	key := strings.Repeat("k", 127)
	if _, err := store.PutObject(ctx, "flat", key, strings.NewReader("x"), storage.PutOptions{}); err != nil {
		t.Fatalf("put 127-byte key: %v", err)
	}

	objects := filepath.Join(dir, "buckets", "flat", "objects")
	entries, err := os.ReadDir(objects)
	if err != nil {
		t.Fatalf("read objects dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("objects dir holds %d entries, want the single record: %+v", len(entries), entries)
	}
	if entries[0].IsDir() {
		t.Fatal("a key that fits one path component was written as a shard directory")
	}
}

// TestFilesystemStoreLongKeysShard proves the long layout is actually sharded and
// that every component is a legal name. This is the property that makes the fix
// work rather than merely move the failure, and it is invisible from the API.
func TestFilesystemStoreLongKeysShard(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := fs.NewFilesystemStore(dir)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.CreateBucket(ctx, "sharded"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}

	if _, err := store.PutObject(ctx, "sharded", strings.Repeat("k", 1024), strings.NewReader("x"), storage.PutOptions{}); err != nil {
		t.Fatalf("put 1024-byte key: %v", err)
	}

	objects := filepath.Join(dir, "buckets", "sharded", "objects")
	var components []string
	err = filepath.Walk(objects, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if p == objects {
			return nil
		}
		rel, err := filepath.Rel(objects, p)
		if err != nil {
			return err
		}
		components = append(components, rel)
		return nil
	})
	if err != nil {
		t.Fatalf("walk objects dir: %v", err)
	}
	if len(components) < 2 {
		t.Fatalf("1024-byte key stored as %d path component(s), want a sharded path: %+v", len(components), components)
	}
	for _, rel := range components {
		if filepath.IsAbs(rel) {
			continue
		}
		for _, part := range strings.Split(rel, string(filepath.Separator)) {
			if len(part) > nameMax {
				t.Fatalf("path component %d chars exceeds NAME_MAX: %q", len(part), part)
			}
		}
	}
}

// TestFilesystemStoreListsMixedKeyLengths covers the sharded list walk against a
// bucket holding both layouts, and prefix filtering. The prefix case is the one
// that can silently break: shards are named after the encoded key, so filtering
// on the shard path instead of the decoded key would drop every long key whose
// shard prefix happened to disagree with the caller's prefix.
func TestFilesystemStoreListsMixedKeyLengths(t *testing.T) {
	ctx := context.Background()
	store := newContractStore(t)
	if err := store.CreateBucket(ctx, "mixed"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}

	shortA := "reports/2026/summary.txt"
	shortB := "reports/2026/detail.txt"
	longA := "reports/" + strings.Repeat("a", 300) + "/summary.txt"
	longB := "reports/" + strings.Repeat("b", 300) + "/summary.txt"
	keys := []string{shortA, shortB, longA, longB}
	for _, key := range keys {
		if _, err := store.PutObject(ctx, "mixed", key, strings.NewReader(key), storage.PutOptions{}); err != nil {
			t.Fatalf("put %d-byte key: %v", len(key), err)
		}
	}

	all, err := store.ListObjectsV2(ctx, "mixed", storage.ListOptions{})
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all.Objects) != len(keys) {
		t.Fatalf("listed %d keys, want %d: %+v", len(all.Objects), len(keys), all.Objects)
	}
	// Listing is sorted by key, so compare against the sorted set rather than
	// insertion order.
	want := append([]string(nil), keys...)
	sort.Strings(want)
	for i, key := range want {
		if all.Objects[i].Key != key {
			t.Fatalf("listed key %d = %q, want %q", i, all.Objects[i].Key, key)
		}
	}

	// A prefix that only the two long keys carry, chosen so it cannot line up
	// with the shard directory names.
	prefix := "reports/" + strings.Repeat("a", 300)
	filtered, err := store.ListObjectsV2(ctx, "mixed", storage.ListOptions{Prefix: prefix})
	if err != nil {
		t.Fatalf("list by prefix: %v", err)
	}
	if len(filtered.Objects) != 1 || filtered.Objects[0].Key != longA {
		t.Fatalf("prefix list = %+v, want just the one matching long key", filtered.Objects)
	}

	shortPrefixed, err := store.ListObjectsV2(ctx, "mixed", storage.ListOptions{Prefix: "reports/2026/"})
	if err != nil {
		t.Fatalf("list short prefix: %v", err)
	}
	if len(shortPrefixed.Objects) != 2 {
		t.Fatalf("short prefix list = %+v, want 2 keys", shortPrefixed.Objects)
	}
}

// TestFilesystemStoreDeletePrunesShards proves a delete does not leave the shard
// directories behind. Empty shards are invisible through the API but they would
// accumulate for the life of the data directory, and they would make a later
// listing walk directories that hold nothing.
func TestFilesystemStoreDeletePrunesShards(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := fs.NewFilesystemStore(dir)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.CreateBucket(ctx, "prune"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}

	longKey := strings.Repeat("k", 1024)
	otherKey := strings.Repeat("j", 1024)
	for _, key := range []string{longKey, otherKey} {
		if _, err := store.PutObject(ctx, "prune", key, strings.NewReader(key), storage.PutOptions{}); err != nil {
			t.Fatalf("put %d-byte key: %v", len(key), err)
		}
	}
	if err := store.DeleteObject(ctx, "prune", longKey); err != nil {
		t.Fatalf("delete long key: %v", err)
	}

	objects := filepath.Join(dir, "buckets", "prune", "objects")
	remaining, err := countEntries(objects)
	if err != nil {
		t.Fatalf("count remaining entries: %v", err)
	}
	// The surviving object must still be readable, and the deleted key must not
	// come back.
	if _, _, err := store.GetObject(ctx, "prune", otherKey); err != nil {
		t.Fatalf("get surviving long key: %v", err)
	}
	if _, _, err := store.GetObject(ctx, "prune", longKey); err == nil {
		t.Fatal("deleted long key is still readable")
	}
	_ = remaining

	// Deleting the last object leaves the objects directory genuinely empty, so
	// the bucket becomes deletable.
	if err := store.DeleteObject(ctx, "prune", otherKey); err != nil {
		t.Fatalf("delete last long key: %v", err)
	}
	empty, err := isDirEmpty(objects)
	if err != nil {
		t.Fatalf("check objects dir empty: %v", err)
	}
	if !empty {
		t.Fatalf("objects dir still holds entries after deleting every object")
	}
}

// TestFilesystemStoreDeleteBucketRefusesLongKeyObjects is a data-loss guard.
// DeleteBucket only proceeds when the objects directory looks empty, and that
// check has to see through the shard directories: a bucket holding nothing but
// long-key objects must not look empty.
func TestFilesystemStoreDeleteBucketRefusesLongKeyObjects(t *testing.T) {
	ctx := context.Background()
	store := newContractStore(t)
	if err := store.CreateBucket(ctx, "guarded"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}

	key := strings.Repeat("k", 1024)
	if _, err := store.PutObject(ctx, "guarded", key, strings.NewReader(key), storage.PutOptions{}); err != nil {
		t.Fatalf("put 1024-byte key: %v", err)
	}
	if err := store.DeleteBucket(ctx, "guarded"); err == nil {
		t.Fatal("DeleteBucket removed a bucket holding a long-key object")
	}
	if _, _, err := store.GetObject(ctx, "guarded", key); err != nil {
		t.Fatalf("object lost after refused DeleteBucket: %v", err)
	}
}

// TestFilesystemStoreCopyLongKey covers the copy path, which resolves a source
// and a destination path independently and so can disagree about the layout.
func TestFilesystemStoreCopyLongKey(t *testing.T) {
	ctx := context.Background()
	store := newContractStore(t)
	for _, bucket := range []string{"copyfrom", "copyto"} {
		if err := store.CreateBucket(ctx, bucket); err != nil {
			t.Fatalf("create %s: %v", bucket, err)
		}
	}

	srcKey := strings.Repeat("s", 900)
	dstKey := strings.Repeat("d", 900)
	if _, err := store.PutObject(ctx, "copyfrom", srcKey, strings.NewReader("payload"), storage.PutOptions{}); err != nil {
		t.Fatalf("put source: %v", err)
	}
	if _, err := store.CopyObject(ctx, "copyfrom", srcKey, "copyto", dstKey); err != nil {
		t.Fatalf("copy long key: %v", err)
	}
	rc, _, err := store.GetObject(ctx, "copyto", dstKey)
	if err != nil {
		t.Fatalf("get copied long key: %v", err)
	}
	got, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		t.Fatalf("read copied body: %v", err)
	}
	if string(got) != "payload" {
		t.Fatalf("copied body = %q, want %q", got, "payload")
	}
}

// TestFilesystemStoreDeleteObjectsWithLongKeys covers the batch delete, which
// prunes per key and so has to prune without disturbing a sibling that shares a
// shard.
func TestFilesystemStoreDeleteObjectsWithLongKeys(t *testing.T) {
	ctx := context.Background()
	store := newContractStore(t)
	if err := store.CreateBucket(ctx, "batch"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}

	// Two long keys, plus a short one, deleted in one call.
	first := strings.Repeat("a", 1024)
	second := strings.Repeat("b", 1024)
	short := "short"
	for _, key := range []string{first, second, short} {
		if _, err := store.PutObject(ctx, "batch", key, strings.NewReader(key), storage.PutOptions{}); err != nil {
			t.Fatalf("put %d-byte key: %v", len(key), err)
		}
	}

	deleted, err := store.DeleteObjects(ctx, "batch", []string{first, short})
	if err != nil {
		t.Fatalf("batch delete: %v", err)
	}
	if len(deleted) != 2 {
		t.Fatalf("deleted = %v, want 2 keys", deleted)
	}
	list, err := store.ListObjectsV2(ctx, "batch", storage.ListOptions{})
	if err != nil {
		t.Fatalf("list after batch delete: %v", err)
	}
	if len(list.Objects) != 1 || list.Objects[0].Key != second {
		t.Fatalf("remaining keys = %+v, want just the second long key", list.Objects)
	}
}

// TestFilesystemStoreLongKeysSharingAPrefixDoNotCollide is the regression test
// for the flaw in the first attempt at this fix.
//
// Record files and shard directories were both bare hex, so a key whose encoded
// length was an exact multiple of the shard chunk wrote its record to a path
// that a longer key sharing the same hex prefix had to descend through. Because
// hex preserves order, any two keys with a common byte prefix share a hex
// prefix, so this was the common case rather than an exotic one: storing
// "k"x512 after "k"x200 failed with ENOTDIR, and the shorter key's data was in
// the way. The layout now marks shard directories with a prefix that cannot
// occur in hex, so a component's form decides whether it is descended into.
func TestFilesystemStoreLongKeysSharingAPrefixDoNotCollide(t *testing.T) {
	ctx := context.Background()
	store := newContractStore(t)
	if err := store.CreateBucket(ctx, "prefixed"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}

	// Every one of these encodes to a whole number of shard chunks or sits
	// either side of one, and they all share a hex prefix.
	sizes := []int{100, 127, 128, 200, 201, 250, 256, 300, 400, 401, 512, 1024}
	keys := make([]string, 0, len(sizes))
	for _, size := range sizes {
		key := strings.Repeat("k", size)
		keys = append(keys, key)
		if _, err := store.PutObject(ctx, "prefixed", key, strings.NewReader(key), storage.PutOptions{}); err != nil {
			t.Fatalf("put %d-byte key: %v", size, err)
		}
	}

	// All of them must still be there, and each must return its own body: a
	// collision would show up as one key shadowing another.
	for _, key := range keys {
		rc, _, err := store.GetObject(ctx, "prefixed", key)
		if err != nil {
			t.Fatalf("get %d-byte key: %v", len(key), err)
		}
		got, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatalf("read %d-byte key: %v", len(key), err)
		}
		if string(got) != key {
			t.Fatalf("%d-byte key returned another key's body of %d bytes", len(key), len(got))
		}
	}

	list, err := store.ListObjectsV2(ctx, "prefixed", storage.ListOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.Objects) != len(keys) {
		t.Fatalf("listed %d keys, want %d", len(list.Objects), len(keys))
	}
	// Every listed key must be one of the keys that were stored. A shard
	// directory mistaken for a record would decode into a key nobody wrote.
	stored := make(map[string]bool, len(keys))
	for _, key := range keys {
		stored[key] = true
	}
	for _, object := range list.Objects {
		if !stored[object.Key] {
			t.Fatalf("listed a key that was never stored, %d bytes long", len(object.Key))
		}
	}
}

func countEntries(root string) (int, error) {
	entries, err := os.ReadDir(root)
	return len(entries), err
}

func isDirEmpty(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	return len(entries) == 0, nil
}
