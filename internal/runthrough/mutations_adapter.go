package runthrough

import (
	"context"
	"errors"
	"io"

	"github.com/chester-hill-solutions/stow/internal/storage"
)

func (a *Adapter) DeleteObject(ctx context.Context, bucket, key string) error {
	action := a.decideUpstreamWrite(bucket)
	if action == writeError {
		return ErrLiveWritesDisabled
	}
	version := ""
	if action == writePropagate {
		version = a.localVersion(ctx, bucket, key)
	}
	if err := a.local.DeleteObject(ctx, bucket, key); err != nil && !errors.Is(err, storage.ErrObjectNotFound) {
		return err
	}
	a.invalidateCache(ctx, bucket, key)
	switch action {
	case writeSkip:
		return nil
	case writePropagate:
		entry, err := a.enqueueIntent(ctx, OutboxDelete, bucket, key, version)
		if err != nil {
			return err
		}
		return a.completeIntent(ctx, entry)
	default:
		return nil
	}
}

func (a *Adapter) DeleteObjects(ctx context.Context, bucket string, keys []string) ([]string, error) {
	action := a.decideUpstreamWrite(bucket)
	if action == writeError {
		return nil, ErrLiveWritesDisabled
	}
	versions := map[string]string{}
	if action == writePropagate {
		for _, key := range keys {
			versions[key] = a.localVersion(ctx, bucket, key)
		}
	}
	deleted, err := a.local.DeleteObjects(ctx, bucket, keys)
	if err != nil {
		return deleted, err
	}
	if a.separateCache {
		for _, key := range deleted {
			a.invalidateCache(ctx, bucket, key)
		}
	}
	switch action {
	case writeSkip:
		return deleted, nil
	case writePropagate:
		for _, key := range deleted {
			entry, err := a.enqueueIntent(ctx, OutboxDelete, bucket, key, versions[key])
			if err != nil {
				return deleted, err
			}
			if err := a.completeIntent(ctx, entry); err != nil {
				return deleted, err
			}
		}
		return deleted, nil
	default:
		return deleted, nil
	}
}

func (a *Adapter) CopyObject(ctx context.Context, srcBucket, srcKey, dstBucket, dstKey string) (*storage.ObjectMeta, error) {
	meta, err := a.local.CopyObject(ctx, srcBucket, srcKey, dstBucket, dstKey)
	if err != nil {
		return nil, err
	}
	a.invalidateCache(ctx, dstBucket, dstKey)
	action := a.decideUpstreamWrite(dstBucket)
	switch action {
	case writeSkip:
		return meta, nil
	case writeError:
		return meta, ErrLiveWritesDisabled
	case writePropagate:
		entry, err := a.enqueueIntent(ctx, OutboxPut, dstBucket, dstKey)
		if err != nil {
			return meta, err
		}
		if err := a.completeIntent(ctx, entry); err != nil {
			return meta, err
		}
		return meta, nil
	default:
		return meta, nil
	}
}

func (a *Adapter) CreateMultipartUpload(ctx context.Context, bucket, key string) (*storage.MultipartUpload, error) {
	return a.local.CreateMultipartUpload(ctx, bucket, key)
}

func (a *Adapter) UploadPart(ctx context.Context, uploadID string, partNumber int, body io.Reader) (*storage.PartInfo, error) {
	return a.local.UploadPart(ctx, uploadID, partNumber, body)
}

func (a *Adapter) CompleteMultipartUpload(ctx context.Context, uploadID string, parts []storage.PartInfo) (*storage.ObjectMeta, error) {
	meta, err := a.local.CompleteMultipartUpload(ctx, uploadID, parts)
	if err != nil {
		return nil, err
	}
	action := a.decideUpstreamWrite(meta.Bucket)
	switch action {
	case writeSkip:
		return meta, nil
	case writeError:
		return meta, ErrLiveWritesDisabled
	case writePropagate:
		entry, err := a.enqueueIntent(ctx, OutboxPut, meta.Bucket, meta.Key)
		if err != nil {
			return meta, err
		}
		if err := a.completeIntent(ctx, entry); err != nil {
			return meta, err
		}
		return meta, nil
	default:
		return meta, nil
	}
}
