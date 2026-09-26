package runthrough

import (
	"context"

	"github.com/chester-hill-solutions/stow-s3/internal/storage"
)

func (a *Adapter) CreateMultipartUpload(ctx context.Context, bucket, key string) (*storage.MultipartUpload, error) {
	if a.localMultipart == nil {
		return nil, storage.ErrMultipartUnsupported
	}
	return a.localMultipart.CreateMultipartUpload(ctx, bucket, key)
}

func (a *Adapter) GetMultipartUpload(ctx context.Context, uploadID string) (*storage.MultipartUpload, error) {
	if a.localMultipart == nil {
		return nil, storage.ErrMultipartUnsupported
	}
	return a.localMultipart.GetMultipartUpload(ctx, uploadID)
}

func (a *Adapter) AbortMultipartUpload(ctx context.Context, uploadID string) error {
	if a.localMultipart == nil {
		return storage.ErrMultipartUnsupported
	}
	return a.localMultipart.AbortMultipartUpload(ctx, uploadID)
}

func (a *Adapter) ListParts(ctx context.Context, uploadID string) ([]storage.PartInfo, error) {
	if a.localMultipart == nil {
		return nil, storage.ErrMultipartUnsupported
	}
	return a.localMultipart.ListParts(ctx, uploadID)
}

func (a *Adapter) ValidateMultipartUpload(ctx context.Context, uploadID, bucket, key string) error {
	if a.localMultipart == nil {
		return storage.ErrMultipartUnsupported
	}
	return a.localMultipart.ValidateMultipartUpload(ctx, uploadID, bucket, key)
}

func (a *Adapter) ListMultipartUploads(ctx context.Context, bucket string, opts storage.MultipartListOptions) (*storage.MultipartListResult, error) {
	if a.localMultipart == nil {
		return nil, storage.ErrMultipartUnsupported
	}
	return a.localMultipart.ListMultipartUploads(ctx, bucket, opts)
}
