package runthrough

import (
	"context"

	"github.com/chester-hill-solutions/stow/internal/storage"
)

func (a *Adapter) AbortMultipartUpload(ctx context.Context, uploadID string) error {
	return a.local.AbortMultipartUpload(ctx, uploadID)
}

func (a *Adapter) ListParts(ctx context.Context, uploadID string) ([]storage.PartInfo, error) {
	return a.local.ListParts(ctx, uploadID)
}

func (a *Adapter) ValidateMultipartUpload(ctx context.Context, uploadID, bucket, key string) error {
	return a.local.ValidateMultipartUpload(ctx, uploadID, bucket, key)
}

func (a *Adapter) ListMultipartUploads(ctx context.Context, bucket string, opts storage.MultipartListOptions) (*storage.MultipartListResult, error) {
	return a.local.ListMultipartUploads(ctx, bucket, opts)
}
