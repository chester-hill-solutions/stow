package storage

import (
	"context"
	"io"
)

// Store is the core object storage interface.
type Store interface {
	CreateBucket(ctx context.Context, name string) error
	DeleteBucket(ctx context.Context, name string) error
	HeadBucket(ctx context.Context, name string) (*BucketInfo, error)
	ListBuckets(ctx context.Context) ([]BucketInfo, error)

	PutObject(ctx context.Context, bucket, key string, body io.Reader, opts PutOptions) (*ObjectMeta, error)
	GetObject(ctx context.Context, bucket, key string) (io.ReadCloser, *ObjectMeta, error)
	HeadObject(ctx context.Context, bucket, key string) (*ObjectMeta, error)
	DeleteObject(ctx context.Context, bucket, key string) error
	DeleteObjects(ctx context.Context, bucket string, keys []string) ([]string, error)
	CopyObject(ctx context.Context, srcBucket, srcKey, dstBucket, dstKey string) (*ObjectMeta, error)
	ListObjectsV2(ctx context.Context, bucket string, opts ListOptions) (*ListResult, error)

	CreateMultipartUpload(ctx context.Context, bucket, key string) (*MultipartUpload, error)
	GetMultipartUpload(ctx context.Context, uploadID string) (*MultipartUpload, error)
	UploadPart(ctx context.Context, uploadID string, partNumber int, body io.Reader) (*PartInfo, error)
	CompleteMultipartUpload(ctx context.Context, uploadID string, parts []PartInfo) (*ObjectMeta, error)
	AbortMultipartUpload(ctx context.Context, uploadID string) error
	ListParts(ctx context.Context, uploadID string) ([]PartInfo, error)
	ValidateMultipartUpload(ctx context.Context, uploadID, bucket, key string) error
	ListMultipartUploads(ctx context.Context, bucket string, opts MultipartListOptions) (*MultipartListResult, error)
	Close() error
}
