package stow

import (
	"bytes"
	"context"
	"errors"
	"io"
	"time"

	"github.com/chester-hill-solutions/stow-s3/internal/storage"
)

// Store is where an environment's objects live.
//
// It exists so that Open is not limited to the memory backend. Until this
// interface there was exactly one thing you could do with an embedded runtime —
// open the memory one — and a filesystem or workspace runtime had to be reached
// through a second constructor, which is the fork this removes. The architecture
// calls for changing one line to get a different store:
//
//	Store: Filesystem(path)
//
// and that was not true of the public API. It is now.
//
// The surface is the object model and nothing else. It is deliberately not the
// internal storage contract: no HTTP-shaped vocabulary, no io.Reader bodies, no
// multipart. A caller supplying storage should not have to import an internal
// package to do it, and should not have to implement nine multipart methods for
// a store that will never serve one.
//
// Multipart is therefore an OPTIONAL interface, below. A store that implements
// MultipartStore gets multipart; one that does not is told so through the
// environment's capabilities rather than by failing an upload partway through.
type Store interface {
	CreateBucket(ctx context.Context, name string) error
	DeleteBucket(ctx context.Context, name string) error
	ListBuckets(ctx context.Context) ([]Bucket, error)

	PutObject(ctx context.Context, bucket, key string, data []byte, options PutOptions) (Object, error)
	GetObject(ctx context.Context, bucket, key string) (Object, error)
	HeadObject(ctx context.Context, bucket, key string) (Object, error)
	DeleteObject(ctx context.Context, bucket, key string) error
	ListObjects(ctx context.Context, bucket string, options ListOptions) (ObjectPage, error)

	// Close releases the store. The runtime calls it when the environment is
	// closed, so a store is closed exactly once regardless of who owns it.
	Close() error
}

// MultipartStore is the optional extension: a store that can stream an object
// larger than memory.
//
// It is separate from Store because multipart is how one protocol happens to do
// that. Requiring it would put that shape into the object model, which is the
// coupling the architecture document's store section exists to name — so a store
// that will never serve a large object implements nine fewer methods.
//
// It is the WHOLE surface, not just the write half. The runtime reconciles
// in-flight uploads when it opens, so a store claiming multipart has to be able
// to answer "what is in flight?" as well as "start one". An earlier version of
// this interface had only the write half and the adapter answered the read half
// with empty results, which is a lie the moment a caller asks.
type MultipartStore interface {
	// The write half.
	CreateMultipartUpload(ctx context.Context, bucket, key string) (MultipartUpload, error)
	UploadPart(ctx context.Context, uploadID string, partNumber int, data []byte) (Part, error)
	CompleteMultipartUpload(ctx context.Context, uploadID string, parts []Part) (Object, error)
	AbortMultipartUpload(ctx context.Context, uploadID string) error

	// The read half, which the runtime needs at open time to reconcile state it
	// did not create in this process.
	GetMultipartUpload(ctx context.Context, uploadID string) (MultipartUpload, error)
	ListParts(ctx context.Context, uploadID string) ([]Part, error)
	ListMultipartUploads(ctx context.Context, bucket string) ([]MultipartUpload, error)
}

// MultipartUpload is an in-flight upload.
type MultipartUpload struct {
	UploadID  string
	Bucket    string
	Key       string
	Initiated time.Time
}

// Part is one uploaded piece of a multipart upload. ETag is what the completing
// call identifies the part by, so it is required.
type Part struct {
	PartNumber int
	ETag       string
	Size       int64
}

// storeAdapter presents a public Store as the internal storage contract, so a
// caller can supply storage without importing internal/ and the internal
// contract keeps its own shape.
type storeAdapter struct{ store Store }

var _ storage.Store = (*storeAdapter)(nil)

// storeWithMultipart is the internal contract for a caller's store that also
// serves multipart. Both halves are present only because the caller's store has
// both, so the runtime's discovery finds exactly what the caller supplied.
type storeWithMultipart struct {
	storage.Store
	storage.MultipartStore
}

// adaptStore returns the internal contract for a caller's store. Multipart is
// carried only when the caller's store carries it, so the internal contract's
// optional half is discovered by the runtime rather than declared to it.
func adaptStore(store Store) storage.Store {
	adapter := &storeAdapter{store: store}
	multi, ok := store.(MultipartStore)
	if !ok {
		return adapter
	}
	return &storeWithMultipart{
		Store:          adapter,
		MultipartStore: multipartAdapter{multi: multi},
	}
}

func (a *storeAdapter) CreateBucket(ctx context.Context, name string) error {
	return translate(a.store.CreateBucket(ctx, name))
}

func (a *storeAdapter) DeleteBucket(ctx context.Context, name string) error {
	return translate(a.store.DeleteBucket(ctx, name))
}

// translate maps the public sentinels onto the internal ones.
//
// A store that returns "no such key" as a fresh error looks exactly like a store
// reporting a fault, and the runtime cannot tell them apart: its quota
// pre-check asks for an object's size before writing it, and treats a miss as
// zero and anything else as a failure. So the whole first write to a supplied
// store fails for a reason that has nothing to do with writing.
//
// The translation is here rather than demanded of the caller because a caller
// implementing Store should not have to know that internal/storage has sentinels
// at all. The public vocabulary is the documented one: return ErrBucketNotFound
// or ErrObjectNotFound from pkg/stow, and anything else is treated as a fault.
func translate(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrBucketNotFound):
		return storage.ErrBucketNotFound
	case errors.Is(err, ErrObjectNotFound):
		return storage.ErrObjectNotFound
	case errors.Is(err, ErrBucketExists):
		return storage.ErrBucketExists
	case errors.Is(err, ErrBucketNotEmpty):
		return storage.ErrBucketNotEmpty
	case errors.Is(err, ErrInvalidBucket):
		return storage.ErrInvalidBucketName
	case errors.Is(err, ErrInvalidKey):
		return storage.ErrInvalidKey
	default:
		return err
	}
}

func (a *storeAdapter) ListBuckets(ctx context.Context) ([]storage.BucketInfo, error) {
	buckets, err := a.store.ListBuckets(ctx)
	if err != nil {
		return nil, translate(err)
	}
	out := make([]storage.BucketInfo, 0, len(buckets))
	for _, bucket := range buckets {
		out = append(out, storage.BucketInfo{Name: bucket.Name, CreationDate: bucket.CreationDate})
	}
	return out, nil
}

func (a *storeAdapter) HeadBucket(ctx context.Context, name string) (*storage.BucketInfo, error) {
	buckets, err := a.store.ListBuckets(ctx)
	if err != nil {
		return nil, translate(err)
	}
	for _, bucket := range buckets {
		if bucket.Name == name {
			return &storage.BucketInfo{Name: bucket.Name, CreationDate: bucket.CreationDate}, nil
		}
	}
	// The bare sentinel, not a wrapped form. The run-through adapter tells a
	// missing bucket from a missing key by exactly this error, and a
	// caller-supplied store must not change that vocabulary underneath it.
	return nil, storage.ErrBucketNotFound
}

func (a *storeAdapter) PutObject(ctx context.Context, bucket, key string, body io.Reader, options storage.PutOptions) (*storage.ObjectMeta, error) {
	data, err := io.ReadAll(body)
	if err != nil {
		return nil, err
	}
	object, err := a.store.PutObject(ctx, bucket, key, data, PutOptions{
		ContentType: options.ContentType,
		Metadata:    storage.CloneMetadata(options.Metadata),
	})
	if err != nil {
		return nil, translate(err)
	}
	return objectMetaOf(object), nil
}

func (a *storeAdapter) GetObject(ctx context.Context, bucket, key string) (io.ReadCloser, *storage.ObjectMeta, error) {
	object, err := a.store.GetObject(ctx, bucket, key)
	if err != nil {
		return nil, nil, translate(err)
	}
	// bytes.NewReader does not copy, so handing the slice to a caller that may or
	// may not keep it costs nothing extra.
	return io.NopCloser(bytes.NewReader(object.Data)), objectMetaOf(object), nil
}

func (a *storeAdapter) HeadObject(ctx context.Context, bucket, key string) (*storage.ObjectMeta, error) {
	object, err := a.store.HeadObject(ctx, bucket, key)
	if err != nil {
		return nil, translate(err)
	}
	return objectMetaOf(object), nil
}

func (a *storeAdapter) DeleteObject(ctx context.Context, bucket, key string) error {
	return translate(a.store.DeleteObject(ctx, bucket, key))
}

func (a *storeAdapter) ListObjectsV2(ctx context.Context, bucket string, options storage.ListOptions) (*storage.ListResult, error) {
	page, err := a.store.ListObjects(ctx, bucket, ListOptions{
		Prefix: options.Prefix,
		Cursor: options.ContinuationToken,
		Limit:  options.MaxKeys,
	})
	if err != nil {
		return nil, translate(err)
	}
	result := &storage.ListResult{
		Objects:               make([]storage.ObjectMeta, 0, len(page.Objects)),
		IsTruncated:           page.Truncated,
		ContinuationToken:     options.ContinuationToken,
		NextContinuationToken: page.NextCursor,
	}
	for _, object := range page.Objects {
		result.Objects = append(result.Objects, *objectMetaOf(object))
	}
	return result, nil
}

// DeleteObjects and CopyObject are expressed through the primitives rather than
// refused: both are exactly what they look like, and a store that cannot do them
// atomically can still do them correctly one at a time. That is a real semantic
// difference from a native store and is why the internal contract keeps its own
// versions of these two methods.
func (a *storeAdapter) DeleteObjects(ctx context.Context, bucket string, keys []string) ([]string, error) {
	deleted := make([]string, 0, len(keys))
	for _, key := range keys {
		if err := a.store.DeleteObject(ctx, bucket, key); err != nil {
			return deleted, translate(err)
		}
		deleted = append(deleted, key)
	}
	return deleted, nil
}

func (a *storeAdapter) CopyObject(ctx context.Context, srcBucket, srcKey, dstBucket, dstKey string) (*storage.ObjectMeta, error) {
	source, err := a.store.GetObject(ctx, srcBucket, srcKey)
	if err != nil {
		return nil, translate(err)
	}
	copied, err := a.store.PutObject(ctx, dstBucket, dstKey, source.Data, PutOptions{
		ContentType: source.ContentType,
		Metadata:    source.Metadata,
	})
	if err != nil {
		return nil, translate(err)
	}
	return objectMetaOf(copied), nil
}

func (a *storeAdapter) Close() error { return a.store.Close() }

// multipartAdapter presents a caller's MultipartStore as the internal contract.
// It is reached only when the caller's store implements MultipartStore, so
// there is no "unsupported" case here to answer.
type multipartAdapter struct{ multi MultipartStore }

var _ storage.MultipartStore = multipartAdapter{}

func (a multipartAdapter) CreateMultipartUpload(ctx context.Context, bucket, key string) (*storage.MultipartUpload, error) {
	upload, err := a.multi.CreateMultipartUpload(ctx, bucket, key)
	if err != nil {
		return nil, err
	}
	return uploadMetaOf(upload), nil
}

func (a multipartAdapter) GetMultipartUpload(ctx context.Context, uploadID string) (*storage.MultipartUpload, error) {
	upload, err := a.multi.GetMultipartUpload(ctx, uploadID)
	if err != nil {
		return nil, err
	}
	return uploadMetaOf(upload), nil
}

func (a multipartAdapter) UploadPart(ctx context.Context, uploadID string, partNumber int, body io.Reader) (*storage.PartInfo, error) {
	data, err := io.ReadAll(body)
	if err != nil {
		return nil, err
	}
	part, err := a.multi.UploadPart(ctx, uploadID, partNumber, data)
	if err != nil {
		return nil, err
	}
	return partInfoOf(part), nil
}

func (a multipartAdapter) CompleteMultipartUpload(ctx context.Context, uploadID string, parts []storage.PartInfo) (*storage.ObjectMeta, error) {
	supplied := make([]Part, 0, len(parts))
	for _, part := range parts {
		supplied = append(supplied, Part{PartNumber: part.PartNumber, ETag: part.ETag, Size: part.Size})
	}
	object, err := a.multi.CompleteMultipartUpload(ctx, uploadID, supplied)
	if err != nil {
		return nil, err
	}
	return objectMetaOf(object), nil
}

func (a multipartAdapter) AbortMultipartUpload(ctx context.Context, uploadID string) error {
	return a.multi.AbortMultipartUpload(ctx, uploadID)
}

func (a multipartAdapter) ListParts(ctx context.Context, uploadID string) ([]storage.PartInfo, error) {
	parts, err := a.multi.ListParts(ctx, uploadID)
	if err != nil {
		return nil, err
	}
	out := make([]storage.PartInfo, 0, len(parts))
	for _, part := range parts {
		out = append(out, *partInfoOf(part))
	}
	return out, nil
}

// ValidateMultipartUpload is the one method with no public equivalent: the
// internal contract asks a store to confirm an upload is the one a completing
// call claims, so the upload is fetched and compared.
func (a multipartAdapter) ValidateMultipartUpload(ctx context.Context, uploadID, bucket, key string) error {
	upload, err := a.multi.GetMultipartUpload(ctx, uploadID)
	if err != nil {
		return err
	}
	if upload.Bucket != bucket || upload.Key != key {
		return storage.ErrUploadNotFound
	}
	return nil
}

func (a multipartAdapter) ListMultipartUploads(ctx context.Context, bucket string, opts storage.MultipartListOptions) (*storage.MultipartListResult, error) {
	uploads, err := a.multi.ListMultipartUploads(ctx, bucket)
	if err != nil {
		return nil, err
	}
	result := &storage.MultipartListResult{}
	for _, upload := range uploads {
		result.Uploads = append(result.Uploads, *uploadMetaOf(upload))
	}
	return result, nil
}

func partInfoOf(part Part) *storage.PartInfo {
	return &storage.PartInfo{PartNumber: part.PartNumber, ETag: part.ETag, Size: part.Size}
}

func uploadMetaOf(upload MultipartUpload) *storage.MultipartUpload {
	return &storage.MultipartUpload{
		UploadID:  upload.UploadID,
		Bucket:    upload.Bucket,
		Key:       upload.Key,
		Initiated: upload.Initiated,
	}
}

func objectMetaOf(object Object) *storage.ObjectMeta {
	return &storage.ObjectMeta{
		Bucket:       object.Bucket,
		Key:          object.Key,
		Size:         object.Size,
		ETag:         object.ETag,
		ContentType:  object.ContentType,
		Metadata:     storage.CloneMetadata(object.Metadata),
		LastModified: object.LastModified,
	}
}
