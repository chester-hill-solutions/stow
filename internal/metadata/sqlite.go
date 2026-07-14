package metadata

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/chester-hill-solutions/stow/internal/storage"
	_ "modernc.org/sqlite"
)

// DB indexes bucket/object/multipart metadata in SQLite.
type DB struct {
	db *sql.DB
}

// Open opens (or creates) a SQLite metadata database at path.
func Open(path string) (*DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		_ = db.Close()
		return nil, err
	}
	m := &DB{db: db}
	if err := m.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return m, nil
}

// Close closes the database.
func (m *DB) Close() error {
	return m.db.Close()
}

func (m *DB) migrate() error {
	schema := `
CREATE TABLE IF NOT EXISTS buckets (
	name TEXT PRIMARY KEY,
	created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS objects (
	bucket TEXT NOT NULL,
	key TEXT NOT NULL,
	size INTEGER NOT NULL,
	etag TEXT NOT NULL,
	content_type TEXT,
	last_modified INTEGER NOT NULL,
	metadata TEXT,
	PRIMARY KEY (bucket, key),
	FOREIGN KEY (bucket) REFERENCES buckets(name) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS multipart_uploads (
	upload_id TEXT PRIMARY KEY,
	bucket TEXT NOT NULL,
	key TEXT NOT NULL,
	initiated_at INTEGER NOT NULL,
	FOREIGN KEY (bucket) REFERENCES buckets(name) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS multipart_parts (
	upload_id TEXT NOT NULL,
	part_number INTEGER NOT NULL,
	etag TEXT NOT NULL,
	size INTEGER NOT NULL,
	last_modified INTEGER NOT NULL,
	PRIMARY KEY (upload_id, part_number),
	FOREIGN KEY (upload_id) REFERENCES multipart_uploads(upload_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_objects_bucket_key ON objects(bucket, key);
CREATE INDEX IF NOT EXISTS idx_multipart_uploads_bucket ON multipart_uploads(bucket);
`
	_, err := m.db.Exec(schema)
	return err
}

// UpsertBucket records a bucket in the metadata index.
func (m *DB) UpsertBucket(_ context.Context, info storage.BucketInfo) error {
	_, err := m.db.Exec(
		`INSERT INTO buckets (name, created_at) VALUES (?, ?)
		 ON CONFLICT(name) DO UPDATE SET created_at = excluded.created_at`,
		info.Name, info.CreationDate.UTC().UnixNano(),
	)
	return err
}

// DeleteBucket removes a bucket from the metadata index.
func (m *DB) DeleteBucket(_ context.Context, name string) error {
	_, err := m.db.Exec(`DELETE FROM buckets WHERE name = ?`, name)
	return err
}

// ListBuckets returns buckets from the metadata index.
func (m *DB) ListBuckets(ctx context.Context) ([]storage.BucketInfo, error) {
	rows, err := m.db.QueryContext(ctx, `SELECT name, created_at FROM buckets ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []storage.BucketInfo
	for rows.Next() {
		var name string
		var created int64
		if err := rows.Scan(&name, &created); err != nil {
			return nil, err
		}
		out = append(out, storage.BucketInfo{
			Name:         name,
			CreationDate: time.Unix(0, created).UTC(),
		})
	}
	return out, rows.Err()
}

// UpsertObject records object metadata.
func (m *DB) UpsertObject(_ context.Context, meta storage.ObjectMeta) error {
	metaJSON, err := json.Marshal(meta.Metadata)
	if err != nil {
		return err
	}
	_, err = m.db.Exec(
		`INSERT INTO objects (bucket, key, size, etag, content_type, last_modified, metadata)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(bucket, key) DO UPDATE SET
		   size = excluded.size,
		   etag = excluded.etag,
		   content_type = excluded.content_type,
		   last_modified = excluded.last_modified,
		   metadata = excluded.metadata`,
		meta.Bucket, meta.Key, meta.Size, meta.ETag, meta.ContentType,
		meta.LastModified.UTC().UnixNano(), string(metaJSON),
	)
	return err
}

// DeleteObject removes object metadata.
func (m *DB) DeleteObject(_ context.Context, bucket, key string) error {
	_, err := m.db.Exec(`DELETE FROM objects WHERE bucket = ? AND key = ?`, bucket, key)
	return err
}

// GetObject returns object metadata when present.
func (m *DB) GetObject(ctx context.Context, bucket, key string) (*storage.ObjectMeta, error) {
	row := m.db.QueryRowContext(ctx,
		`SELECT bucket, key, size, etag, content_type, last_modified, metadata
		 FROM objects WHERE bucket = ? AND key = ?`, bucket, key)

	var meta storage.ObjectMeta
	var lastModified int64
	var metaJSON sql.NullString
	if err := row.Scan(&meta.Bucket, &meta.Key, &meta.Size, &meta.ETag, &meta.ContentType, &lastModified, &metaJSON); err != nil {
		if err == sql.ErrNoRows {
			return nil, storage.ErrObjectNotFound
		}
		return nil, err
	}
	meta.LastModified = time.Unix(0, lastModified).UTC()
	if metaJSON.Valid && metaJSON.String != "" {
		_ = json.Unmarshal([]byte(metaJSON.String), &meta.Metadata)
	}
	return &meta, nil
}

// ListObjects returns object metadata for a bucket using prefix/delimiter pagination.
func (m *DB) ListObjects(ctx context.Context, bucket string, opts storage.ListOptions) (*storage.ListResult, error) {
	rows, err := m.db.QueryContext(ctx,
		`SELECT bucket, key, size, etag, content_type, last_modified, metadata
		 FROM objects WHERE bucket = ? AND key LIKE ? ORDER BY key`,
		bucket, prefixLike(opts.Prefix),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type item struct {
		meta storage.ObjectMeta
		key  string
	}
	var items []item
	for rows.Next() {
		var meta storage.ObjectMeta
		var lastModified int64
		var metaJSON sql.NullString
		if err := rows.Scan(&meta.Bucket, &meta.Key, &meta.Size, &meta.ETag, &meta.ContentType, &lastModified, &metaJSON); err != nil {
			return nil, err
		}
		meta.LastModified = time.Unix(0, lastModified).UTC()
		if metaJSON.Valid && metaJSON.String != "" {
			_ = json.Unmarshal([]byte(metaJSON.String), &meta.Metadata)
		}
		items = append(items, item{meta: meta, key: meta.Key})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	startAfter := opts.StartAfter
	if startAfter == "" {
		startAfter = opts.ContinuationToken
	}
	maxKeys := opts.MaxKeys
	if maxKeys <= 0 {
		maxKeys = 1000
	}

	result := &storage.ListResult{}
	prefixSet := map[string]struct{}{}
	for _, it := range items {
		if startAfter != "" && it.key <= startAfter {
			continue
		}
		if cp := commonPrefixFor(it.key, opts.Prefix, opts.Delimiter); cp != "" {
			if _, ok := prefixSet[cp]; !ok {
				prefixSet[cp] = struct{}{}
				result.CommonPrefixes = append(result.CommonPrefixes, cp)
			}
			continue
		}
		if len(result.Objects) >= maxKeys {
			result.IsTruncated = true
			result.NextContinuationToken = it.key
			break
		}
		result.Objects = append(result.Objects, it.meta)
	}
	result.KeyCount = len(result.Objects) + len(result.CommonPrefixes)
	if opts.ContinuationToken != "" {
		result.ContinuationToken = opts.ContinuationToken
	}
	return result, nil
}

// CreateMultipartUpload records a multipart upload.
func (m *DB) CreateMultipartUpload(_ context.Context, upload storage.MultipartUpload) error {
	_, err := m.db.Exec(
		`INSERT INTO multipart_uploads (upload_id, bucket, key, initiated_at) VALUES (?, ?, ?, ?)`,
		upload.UploadID, upload.Bucket, upload.Key, upload.Initiated.UTC().UnixNano(),
	)
	return err
}

// DeleteMultipartUpload removes multipart upload metadata and parts.
func (m *DB) DeleteMultipartUpload(_ context.Context, uploadID string) error {
	_, err := m.db.Exec(`DELETE FROM multipart_uploads WHERE upload_id = ?`, uploadID)
	return err
}

// UpsertPart records a multipart part.
func (m *DB) UpsertPart(_ context.Context, uploadID string, part storage.PartInfo) error {
	_, err := m.db.Exec(
		`INSERT INTO multipart_parts (upload_id, part_number, etag, size, last_modified)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(upload_id, part_number) DO UPDATE SET
		   etag = excluded.etag,
		   size = excluded.size,
		   last_modified = excluded.last_modified`,
		uploadID, part.PartNumber, part.ETag, part.Size, part.LastModified.UTC().UnixNano(),
	)
	return err
}

// ListParts returns parts for a multipart upload.
func (m *DB) ListParts(ctx context.Context, uploadID string) ([]storage.PartInfo, error) {
	rows, err := m.db.QueryContext(ctx,
		`SELECT part_number, etag, size, last_modified
		 FROM multipart_parts WHERE upload_id = ? ORDER BY part_number`, uploadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []storage.PartInfo
	for rows.Next() {
		var part storage.PartInfo
		var lastModified int64
		if err := rows.Scan(&part.PartNumber, &part.ETag, &part.Size, &lastModified); err != nil {
			return nil, err
		}
		part.LastModified = time.Unix(0, lastModified).UTC()
		out = append(out, part)
	}
	return out, rows.Err()
}

// SyncFromStore rebuilds metadata for all buckets/objects from a Store.
func (m *DB) SyncFromStore(ctx context.Context, store storage.Store) error {
	buckets, err := store.ListBuckets(ctx)
	if err != nil {
		return fmt.Errorf("list buckets: %w", err)
	}
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`DELETE FROM multipart_parts`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM multipart_uploads`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM objects`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM buckets`); err != nil {
		return err
	}

	for _, b := range buckets {
		if _, err := tx.Exec(`INSERT INTO buckets (name, created_at) VALUES (?, ?)`, b.Name, b.CreationDate.UTC().UnixNano()); err != nil {
			return err
		}
		list, err := store.ListObjectsV2(ctx, b.Name, storage.ListOptions{MaxKeys: 100000})
		if err != nil {
			return fmt.Errorf("list objects in %q: %w", b.Name, err)
		}
		for _, obj := range list.Objects {
			metaJSON, err := json.Marshal(obj.Metadata)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(
				`INSERT INTO objects (bucket, key, size, etag, content_type, last_modified, metadata)
				 VALUES (?, ?, ?, ?, ?, ?, ?)`,
				obj.Bucket, obj.Key, obj.Size, obj.ETag, obj.ContentType,
				obj.LastModified.UTC().UnixNano(), string(metaJSON),
			); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func prefixLike(prefix string) string {
	if prefix == "" {
		return "%"
	}
	return prefix + "%"
}

func commonPrefixFor(key, prefix, delimiter string) string {
	if delimiter == "" {
		return ""
	}
	rest := key
	if prefix != "" {
		if len(key) < len(prefix) || key[:len(prefix)] != prefix {
			return ""
		}
		rest = key[len(prefix):]
	}
	idx := indexOf(rest, delimiter)
	if idx < 0 {
		return ""
	}
	return prefix + rest[:idx+len(delimiter)]
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
