package storage

import (
	"context"
	"io"
)

// Store is the core object storage interface: the object model, and nothing else.
//
// Multipart is deliberately not a member. It is how one protocol happens to
// stream an object larger than memory, and requiring it of every store put that
// protocol's shape into the object model. It is the optional interface below.
type Store interface {
	CreateBucket(ctx context.Context, name string) error
	DeleteBucket(ctx context.Context, name string) error
	HeadBucket(ctx context.Context, name string) (*BucketInfo, error)
	ListBuckets(ctx context.Context) ([]BucketInfo, error)

	PutObject(ctx context.Context, bucket, key string, body io.Reader, opts PutOptions) (*ObjectMeta, error)
	GetObject(ctx context.Context, bucket, key string) (io.ReadCloser, *ObjectMeta, error)
	HeadObject(ctx context.Context, bucket, key string) (*ObjectMeta, error)
	DeleteObject(ctx context.Context, bucket, key string) error
	// DeleteObjects deletes each key and returns the keys it deleted, in request
	// order.
	//
	// A key that was not there is neither an error nor a deletion: it is skipped
	// and left out of the returned slice, which is S3's behaviour and what makes
	// a repeated delete idempotent.
	//
	// On failure it returns the keys deleted before the failure alongside the
	// error, so a caller retrying the remainder can skip what already succeeded.
	// Discarding that list turns a recoverable partial failure into a second
	// round of deletes against keys that are gone.
	DeleteObjects(ctx context.Context, bucket string, keys []string) ([]string, error)
	CopyObject(ctx context.Context, srcBucket, srcKey, dstBucket, dstKey string) (*ObjectMeta, error)
	ListObjectsV2(ctx context.Context, bucket string, opts ListOptions) (*ListResult, error)

	Close() error
}

// MultipartStore is the optional extension: a store that can stream an object
// larger than memory.
//
// It is whole rather than write-only because a consumer that reconciles in-flight
// uploads when it opens must be able to ask what is in flight. A write-only
// implementation would force the reader to invent that answer.
type MultipartStore interface {
	CreateMultipartUpload(ctx context.Context, bucket, key string) (*MultipartUpload, error)
	GetMultipartUpload(ctx context.Context, uploadID string) (*MultipartUpload, error)
	UploadPart(ctx context.Context, uploadID string, partNumber int, body io.Reader) (*PartInfo, error)
	CompleteMultipartUpload(ctx context.Context, uploadID string, parts []PartInfo) (*ObjectMeta, error)
	AbortMultipartUpload(ctx context.Context, uploadID string) error
	ListParts(ctx context.Context, uploadID string) ([]PartInfo, error)
	ValidateMultipartUpload(ctx context.Context, uploadID, bucket, key string) error
	ListMultipartUploads(ctx context.Context, bucket string, opts MultipartListOptions) (*MultipartListResult, error)
}
