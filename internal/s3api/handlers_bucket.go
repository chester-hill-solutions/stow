package s3api

import (
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/chester-hill-solutions/stow/internal/storage"
)

func (s *Server) handleListBuckets(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	buckets, err := s.store.ListBuckets(ctx)
	if err != nil {
		writeError(w, r, mapStorageError(err, "/"))
		return
	}
	items := make([]bucketEntry, 0, len(buckets))
	for _, b := range buckets {
		items = append(items, bucketEntry{Name: b.Name, CreationDate: formatTime(b.CreationDate)})
	}
	writeXML(w, r, http.StatusOK, newListBucketsResult(items))
}

func (s *Server) handleCreateBucket(ctx context.Context, w http.ResponseWriter, r *http.Request, bucket string) {
	if !validBucketName(bucket) {
		writeError(w, r, s3Error{Code: "InvalidBucketName", Message: "Invalid bucket name", Resource: "/" + bucket, StatusCode: http.StatusBadRequest})
		return
	}
	err := s.store.CreateBucket(ctx, bucket)
	if err != nil && err != storage.ErrBucketExists {
		writeError(w, r, mapStorageError(err, "/"+bucket))
		return
	}
	w.Header().Set("x-amz-request-id", requestIDFromContext(ctx))
	setCORS(w, r)
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleHeadBucket(ctx context.Context, w http.ResponseWriter, r *http.Request, bucket string) {
	_, err := s.store.HeadBucket(ctx, bucket)
	if err != nil {
		writeError(w, r, mapStorageError(err, "/"+bucket))
		return
	}
	w.Header().Set("x-amz-request-id", requestIDFromContext(ctx))
	setCORS(w, r)
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleDeleteBucket(ctx context.Context, w http.ResponseWriter, r *http.Request, bucket string) {
	err := s.store.DeleteBucket(ctx, bucket)
	if err != nil {
		writeError(w, r, mapStorageError(err, "/"+bucket))
		return
	}
	w.Header().Set("x-amz-request-id", requestIDFromContext(ctx))
	setCORS(w, r)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListObjectsV2(ctx context.Context, w http.ResponseWriter, r *http.Request, bucket string, q url.Values) {
	prefix := q.Get("prefix")
	delimiter := q.Get("delimiter")
	continuation := q.Get("continuation-token")
	startAfter := q.Get("start-after")
	encodingType := q.Get("encoding-type")
	maxKeys := 1000
	if mk := q.Get("max-keys"); mk != "" {
		if n, err := strconv.Atoi(mk); err == nil && n > 0 {
			maxKeys = n
		}
	}

	result, err := s.store.ListObjectsV2(ctx, bucket, storage.ListOptions{
		Prefix:            prefix,
		Delimiter:         delimiter,
		ContinuationToken: continuation,
		MaxKeys:           maxKeys,
		StartAfter:        startAfter,
	})
	if err != nil {
		writeError(w, r, mapStorageError(err, "/"+bucket))
		return
	}

	encodeURL := encodingType == "url"
	resp := listBucketResult{
		Xmlns:                 xmlNS,
		Name:                  bucket,
		Prefix:                prefix,
		KeyCount:              result.KeyCount,
		MaxKeys:               maxKeys,
		IsTruncated:           result.IsTruncated,
		ContinuationToken:     result.ContinuationToken,
		NextContinuationToken: result.NextContinuationToken,
		Delimiter:             delimiter,
	}
	if encodeURL {
		resp.EncodingType = "url"
	}
	for _, o := range result.Objects {
		resp.Contents = append(resp.Contents, objectToEntry(objectMeta{
			Key: o.Key, Size: o.Size, ETag: o.ETag, LastModified: o.LastModified,
		}, encodeURL))
	}
	for _, cp := range result.CommonPrefixes {
		p := cp
		if encodeURL {
			p = urlEncodeKey(cp)
		}
		resp.CommonPrefixes = append(resp.CommonPrefixes, commonPrefix{Prefix: p})
	}
	writeXML(w, r, http.StatusOK, resp)
}

func (s *Server) handlePutObject(ctx context.Context, w http.ResponseWriter, r *http.Request, bucket, key string) {
	cl := r.ContentLength
	if cl < 0 {
		writeError(w, r, s3Error{Code: "InvalidArgument", Message: "Content-Length required", Resource: resourcePath(bucket, key), StatusCode: http.StatusBadRequest})
		return
	}

	contentType := r.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	metadata := extractMetadata(r.Header)
	if len(metadata) > 0 {
		total := metadataSize(metadata)
		if total > 2048 {
			writeError(w, r, s3Error{Code: "InvalidArgument", Message: "Metadata too large", Resource: resourcePath(bucket, key), StatusCode: http.StatusBadRequest})
			return
		}
	}

	meta, err := s.store.PutObject(ctx, bucket, key, r.Body, storage.PutOptions{
		ContentType: contentType,
		Metadata:    metadata,
	})
	if err != nil {
		writeError(w, r, mapStorageError(err, resourcePath(bucket, key)))
		return
	}
	w.Header().Set("ETag", meta.ETag)
	writeXML(w, r, http.StatusOK, putObjectResult{ETag: meta.ETag})
}

func (s *Server) handleGetObject(ctx context.Context, w http.ResponseWriter, r *http.Request, bucket, key string) {
	rc, meta, err := s.store.GetObject(ctx, bucket, key)
	if err != nil {
		writeError(w, r, mapStorageError(err, resourcePath(bucket, key)))
		return
	}
	defer rc.Close()

	rangeHdr := r.Header.Get("Range")
	if rangeHdr != "" {
		s.serveRange(w, r, rc, meta, rangeHdr, bucket, key)
		return
	}

	setObjectHeaders(w, meta)
	setCORS(w, r)
	w.Header().Set("x-amz-request-id", requestIDFromContext(ctx))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, rc)
}

func (s *Server) handleHeadObject(ctx context.Context, w http.ResponseWriter, r *http.Request, bucket, key string) {
	meta, err := s.store.HeadObject(ctx, bucket, key)
	if err != nil {
		writeError(w, r, mapStorageError(err, resourcePath(bucket, key)))
		return
	}
	setObjectHeaders(w, meta)
	setCORS(w, r)
	w.Header().Set("x-amz-request-id", requestIDFromContext(ctx))
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleDeleteObject(ctx context.Context, w http.ResponseWriter, r *http.Request, bucket, key string) {
	err := s.store.DeleteObject(ctx, bucket, key)
	if err != nil && err != storage.ErrObjectNotFound {
		writeError(w, r, mapStorageError(err, resourcePath(bucket, key)))
		return
	}
	w.Header().Set("x-amz-request-id", requestIDFromContext(ctx))
	setCORS(w, r)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteObjects(ctx context.Context, w http.ResponseWriter, r *http.Request, bucket string) {
	var req deleteObjectsRequest
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, s3Error{Code: "MalformedXML", Message: "Malformed XML", Resource: "/"+bucket, StatusCode: http.StatusBadRequest})
		return
	}
	keys := make([]string, 0, len(req.Objects))
	for _, o := range req.Objects {
		keys = append(keys, o.Key)
	}
	deleted, err := s.store.DeleteObjects(ctx, bucket, keys)
	if err != nil {
		writeError(w, r, mapStorageError(err, "/"+bucket))
		return
	}
	resp := deleteResult{}
	for _, k := range deleted {
		resp.Deleted = append(resp.Deleted, deletedEntry{Key: k})
	}
	writeXML(w, r, http.StatusOK, resp)
}

func (s *Server) handleCopyObject(ctx context.Context, w http.ResponseWriter, r *http.Request, dstBucket, dstKey, copySource string) {
	srcBucket, srcKey, err := parseCopySource(copySource)
	if err != nil {
		writeError(w, r, s3Error{Code: "InvalidArgument", Message: err.Error(), Resource: resourcePath(dstBucket, dstKey), StatusCode: http.StatusBadRequest})
		return
	}

	srcMeta, err := s.store.HeadObject(ctx, srcBucket, srcKey)
	if err != nil {
		writeError(w, r, mapStorageError(err, resourcePath(dstBucket, dstKey)))
		return
	}

	if err := checkCopyPreconditions(r.Header, srcMeta); err != nil {
		writeError(w, r, mapStorageError(err, resourcePath(dstBucket, dstKey)))
		return
	}

	meta, err := s.store.CopyObject(ctx, srcBucket, srcKey, dstBucket, dstKey)
	if err != nil {
		writeError(w, r, mapStorageError(err, resourcePath(dstBucket, dstKey)))
		return
	}
	writeXML(w, r, http.StatusOK, copyObjectResult{
		LastModified: formatTime(meta.LastModified),
		ETag:         meta.ETag,
	})
}

func setObjectHeaders(w http.ResponseWriter, meta *storage.ObjectMeta) {
	if meta.ContentType != "" {
		w.Header().Set("Content-Type", meta.ContentType)
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	w.Header().Set("Content-Length", strconv.FormatInt(meta.Size, 10))
	w.Header().Set("ETag", meta.ETag)
	w.Header().Set("Last-Modified", meta.LastModified.UTC().Format(http.TimeFormat))
	for k, v := range meta.Metadata {
		w.Header().Set(k, v)
	}
}

func extractMetadata(h http.Header) map[string]string {
	out := make(map[string]string)
	for k, vals := range h {
		lower := strings.ToLower(k)
		if strings.HasPrefix(lower, "x-amz-meta-") {
			out[k] = vals[0]
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func metadataSize(m map[string]string) int {
	n := 0
	for k, v := range m {
		n += len(k) + len(v)
	}
	return n
}

func validBucketName(name string) bool {
	if len(name) < 3 || len(name) > 63 {
		return false
	}
	for _, c := range name {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
			continue
		}
		return false
	}
	return true
}

func parseCopySource(src string) (bucket, key string, err error) {
	src = strings.TrimPrefix(src, "/")
	parts := strings.SplitN(src, "/", 2)
	if len(parts) != 2 {
		return "", "", errInvalidCopySource
	}
	return parts[0], parts[1], nil
}

var errInvalidCopySource = &copySourceError{"Invalid copy source"}

type copySourceError struct{ msg string }

func (e *copySourceError) Error() string { return e.msg }

func checkCopyPreconditions(h http.Header, meta *storage.ObjectMeta) error {
	if match := h.Get("x-amz-copy-source-if-match"); match != "" && match != meta.ETag {
		return storage.ErrPreconditionFailed
	}
	return nil
}
