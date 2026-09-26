package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/chester-hill-solutions/stow-s3/internal/storage"
)

func (s *Store) PutObject(ctx context.Context, bucket, key string, body io.Reader, opts storage.PutOptions) (*storage.ObjectMeta, error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	if err := storage.ValidateKey(key); err != nil {
		return nil, err
	}
	if !s.bucketExists(bucket) {
		return nil, storage.ErrBucketNotFound
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	return s.putLocked(ctx, bucket, key, body, opts)
}

// putLocked is PutObject's body, split out so that multipart completion, which
// already holds the lock, can assemble an object without deadlocking on a
// non-reentrant mutex.
func (s *Store) putLocked(ctx context.Context, bucket, key string, body io.Reader, opts storage.PutOptions) (*storage.ObjectMeta, error) {
	existing, err := s.peek(ctx, bucket, key)
	if err != nil && !errors.Is(err, storage.ErrObjectNotFound) {
		return nil, err
	}
	if err := storage.CheckWritePreconditions(opts, existing); err != nil {
		return nil, err
	}

	data, err := storage.BytesOf(body)
	if err != nil {
		return nil, err
	}
	if err := storage.VerifyChecksum(opts, data); err != nil {
		return nil, err
	}

	version, err := storage.NewRecordVersion()
	if err != nil {
		return nil, err
	}

	absPath, form := s.pathFor(bucket, key)
	if err := writeFileAtomic(absPath, data); err != nil {
		return nil, fmt.Errorf("workspace store: write %s: %w", key, err)
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return nil, fmt.Errorf("workspace store: stat written %s: %w", key, err)
	}

	entry := ManifestEntry{
		Form:              form,
		Size:              int64(len(data)),
		ETag:              storage.ETagForBytes(data),
		VersionID:         version,
		ContentType:       opts.ContentType,
		Metadata:          storage.CloneMetadata(opts.Metadata),
		ChecksumAlgorithm: storage.NormalizeChecksumAlgorithm(opts.ChecksumAlgorithm),
		ChecksumValue:     opts.ChecksumValue,
		Modified:          info.ModTime().UTC().Truncate(time.Second),
	}
	if form == FormEscaped {
		entry.Digest = Digest(key)
	} else {
		s.folded[foldKey(bucket, key)] = key
	}
	s.manifest.setEntry(bucket, key, entry)
	if err := s.manifest.save(); err != nil {
		return nil, err
	}
	return s.metaFromEntry(bucket, key, entry), nil
}

// GetObject returns an object's bytes. The file may be one stow wrote or one
// the host wrote; the second case is the reason this backend exists.
func (s *Store) GetObject(ctx context.Context, bucket, key string) (io.ReadCloser, *storage.ObjectMeta, error) {
	if err := s.checkOpen(); err != nil {
		return nil, nil, err
	}
	if !s.bucketExists(bucket) {
		return nil, nil, storage.ErrBucketNotFound
	}
	if err := storage.ValidateKey(key); err != nil {
		return nil, nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}

	absPath, info, entry, err := s.resolve(bucket, key)
	if err != nil {
		return nil, nil, err
	}
	file, err := os.Open(absPath)
	if err != nil {
		return nil, nil, storage.ErrObjectNotFound
	}
	return file, s.metaFromEntry(bucket, key, entry, info), nil
}

// HeadObject returns an object's metadata without its bytes, deriving it from
// the file when stow has no manifest entry for it.
func (s *Store) HeadObject(ctx context.Context, bucket, key string) (*storage.ObjectMeta, error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	if !s.bucketExists(bucket) {
		return nil, storage.ErrBucketNotFound
	}
	if err := storage.ValidateKey(key); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	absPath, info, entry, err := s.resolve(bucket, key)
	if err != nil {
		return nil, err
	}
	meta := s.metaFromEntry(bucket, key, entry, info)
	if err := s.absorb(bucket, key, absPath, info, &entry); err != nil {
		return nil, err
	}
	return meta, nil
}

// DeleteObject removes an object's file and forgets it.
func (s *Store) DeleteObject(ctx context.Context, bucket, key string) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	if !s.bucketExists(bucket) {
		return storage.ErrBucketNotFound
	}
	if err := storage.ValidateKey(key); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	absPath, err := s.locate(bucket, key)
	if err != nil {
		return err
	}
	if err := os.Remove(absPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return storage.ErrObjectNotFound
		}
		return fmt.Errorf("workspace store: delete %s: %w", key, err)
	}
	s.pruneEmptyParents(bucket, absPath)
	s.forget(bucket, key)
	return s.manifest.save()
}

// DeleteObjects removes several keys and returns the ones that were not
// removed, which is the S3 delete-many shape.
func (s *Store) DeleteObjects(ctx context.Context, bucket string, keys []string) ([]string, error) {
	if !s.bucketExists(bucket) {
		return nil, storage.ErrBucketNotFound
	}
	// The returned slice is the keys this call deleted. It was the complement
	// until the contract suite pinned the meaning: the S3 handler emits this
	// slice as <Deleted>, so returning the survivors reported the objects still
	// on disk as deleted and stayed silent about the ones actually removed.
	var deleted []string
	for _, key := range keys {
		if err := s.DeleteObject(ctx, bucket, key); err != nil {
			if errors.Is(err, storage.ErrObjectNotFound) {
				// Not an error, and not a deletion. S3 skips it, which is what
				// makes a repeated delete idempotent.
				continue
			}
			return deleted, err
		}
		deleted = append(deleted, key)
	}
	return deleted, nil
}

// CopyObject writes an existing object to a new key, as a real file at the new
// key's natural path where that is possible.
func (s *Store) CopyObject(ctx context.Context, srcBucket, srcKey, dstBucket, dstKey string) (*storage.ObjectMeta, error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	if !s.bucketExists(dstBucket) {
		return nil, storage.ErrBucketNotFound
	}
	if err := storage.ValidateKey(dstKey); err != nil {
		return nil, err
	}
	srcPath, _, _, err := s.resolve(srcBucket, srcKey)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return nil, storage.ErrObjectNotFound
	}
	entry, _ := s.manifest.entry(srcBucket, srcKey)
	opts := storage.PutOptions{
		ContentType:       entry.ContentType,
		Metadata:          entry.Metadata,
		ChecksumAlgorithm: entry.ChecksumAlgorithm,
		ChecksumValue:     entry.ChecksumValue,
	}
	return s.PutObject(ctx, dstBucket, dstKey, bytes.NewReader(data), opts)
}

// ListObjectsV2 lists a bucket's objects: every file in its tree, plus the
// escaped keys the manifest records.
func (s *Store) ListObjectsV2(ctx context.Context, bucket string, opts storage.ListOptions) (*storage.ListResult, error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	if !s.bucketExists(bucket) {
		return nil, storage.ErrBucketNotFound
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	all, err := s.listAll(bucket)
	if err != nil {
		return nil, err
	}
	filtered := make([]storage.ObjectMeta, 0, len(all))
	for _, meta := range all {
		if opts.Prefix == "" || strings.HasPrefix(meta.Key, opts.Prefix) {
			filtered = append(filtered, meta)
		}
	}
	sort.Slice(filtered, func(i, j int) bool { return filtered[i].Key < filtered[j].Key })
	return storage.PaginateObjects(filtered, opts), nil
}

// listAll returns every object in a bucket, from the tree and from the manifest
// for escaped keys.
func (s *Store) listAll(bucket string) ([]storage.ObjectMeta, error) {
	byKey := map[string]storage.ObjectMeta{}
	root := s.bucketDir(bucket)

	walkErr := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		relative = filepath.ToSlash(relative)
		if IsInternal(relative) {
			return nil
		}
		key, ok := KeyFromNaturalPath(relative)
		if !ok {
			return nil
		}
		info, statErr := entry.Info()
		if statErr != nil {
			return nil
		}
		manifestEntry, _ := s.manifest.entry(bucket, key)
		if meta := s.metaFromEntry(bucket, key, manifestEntry, info); meta != nil {
			byKey[key] = *meta
		}
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("workspace store: list %s: %w", bucket, walkErr)
	}

	// Escaped keys live under the internal directory, so the walk above cannot
	// see them. They come from the manifest, and an entry whose file is gone
	// serves as absent rather than as a phantom object.
	for key, entry := range s.manifest.Buckets[bucket] {
		if entry.Form != FormEscaped {
			continue
		}
		if _, known := byKey[key]; known {
			continue
		}
		if _, err := os.Stat(EscapedPath(s.root, key)); err != nil {
			continue
		}
		byKey[key] = *s.metaFromEntry(bucket, key, entry)
	}

	out := make([]storage.ObjectMeta, 0, len(byKey))
	for _, meta := range byKey {
		out = append(out, meta)
	}
	return out, nil
}

// Close flushes the manifest. It never removes anything: a workspace outlives
// the process that opened it, and removal is an explicit destroy.

func (s *Store) metaFromEntry(bucket, key string, entry ManifestEntry, info ...os.FileInfo) *storage.ObjectMeta {
	meta := storage.ObjectMeta{
		Bucket:            bucket,
		Key:               key,
		ETag:              entry.ETag,
		VersionID:         entry.VersionID,
		ContentType:       entry.ContentType,
		LastModified:      entry.Modified,
		Metadata:          storage.CloneMetadata(entry.Metadata),
		ChecksumAlgorithm: entry.ChecksumAlgorithm,
		ChecksumValue:     entry.ChecksumValue,
	}
	if len(info) > 0 {
		if stat := info[0]; stat != nil {
			meta.Size = stat.Size()
			if meta.LastModified.IsZero() {
				meta.LastModified = stat.ModTime().UTC()
			}
		}
	} else {
		meta.Size = entry.Size
	}
	if meta.VersionID == "" {
		meta.VersionID = meta.ETag
	}
	return &meta
}

// detectContentType sniffs a file's type from its first bytes, because a file
// the host wrote has no declared content type and guessing from the extension
// alone gets text/markdown and application/json wrong often enough to matter.
func detectContentType(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return "application/octet-stream"
	}
	defer file.Close()
	head := make([]byte, 512)
	n, _ := io.ReadFull(file, head)
	if n == 0 {
		return "application/octet-stream"
	}
	return http.DetectContentType(head[:n])
}

// pruneEmptyParents removes directories a delete leaves behind, stopping at the
// bucket directory and never crossing into the internal one.
func (s *Store) pruneEmptyParents(bucket, absPath string) {
	root := s.bucketDir(bucket)
	for dir := filepath.Dir(absPath); strings.HasPrefix(dir, root); dir = filepath.Dir(dir) {
		if dir == root {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) > 0 {
			return
		}
		if err := os.Remove(dir); err != nil {
			return
		}
	}
}
