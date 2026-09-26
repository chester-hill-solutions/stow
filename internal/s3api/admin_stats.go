package s3api

import (
	"context"
	"fmt"

	"github.com/chester-hill-solutions/stow-s3/internal/storage"
)

func listAllAdminObjects(ctx context.Context, store storage.Store, bucket string) ([]storage.ObjectMeta, error) {
	var objects []storage.ObjectMeta
	token := ""
	for {
		page, err := store.ListObjectsV2(ctx, bucket, storage.ListOptions{ContinuationToken: token, MaxKeys: 1000})
		if err != nil {
			return nil, err
		}
		objects = append(objects, page.Objects...)
		if !page.IsTruncated {
			return objects, nil
		}
		if page.NextContinuationToken == "" {
			return nil, fmt.Errorf("store returned truncated object listing without a continuation token")
		}
		token = page.NextContinuationToken
	}
}

// listAllAdminUploads pages every in-flight upload in a bucket. A store that
// cannot serve multipart has none, which is a count of zero rather than a fault.
func listAllAdminUploads(ctx context.Context, store storage.MultipartStore, bucket string) ([]storage.MultipartUpload, error) {
	if store == nil {
		return nil, nil
	}
	var uploads []storage.MultipartUpload
	keyMarker := ""
	uploadIDMarker := ""
	for {
		page, err := store.ListMultipartUploads(ctx, bucket, storage.MultipartListOptions{
			KeyMarker:      keyMarker,
			UploadIDMarker: uploadIDMarker,
			MaxUploads:     1000,
		})
		if err != nil {
			return nil, err
		}
		uploads = append(uploads, page.Uploads...)
		if !page.IsTruncated {
			return uploads, nil
		}
		if page.NextKeyMarker == "" && page.NextUploadIDMarker == "" {
			return nil, fmt.Errorf("store returned truncated upload listing without a marker")
		}
		keyMarker = page.NextKeyMarker
		uploadIDMarker = page.NextUploadIDMarker
	}
}
