package s3api

import (
	"context"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/chester-hill-solutions/stow/internal/storage"
)

const minPartSize = 5 * 1024 * 1024

func (s *Server) validateMultipartRoute(ctx context.Context, w http.ResponseWriter, r *http.Request, route routeInfo, uploadID string) bool {
	if err := s.store.ValidateMultipartUpload(ctx, uploadID, route.bucket, route.key); err != nil {
		writeError(w, r, mapStorageError(err, resourcePath(route.bucket, route.key)))
		return false
	}
	return true
}

func (s *Server) handleCreateMultipartUpload(ctx context.Context, w http.ResponseWriter, r *http.Request, bucket, key string) {
	upload, err := s.store.CreateMultipartUpload(ctx, bucket, key)
	if err != nil {
		writeError(w, r, mapStorageError(err, resourcePath(bucket, key)))
		return
	}
	writeXML(w, r, http.StatusOK, initiateMultipartUploadResult{
		Bucket:   bucket,
		Key:      key,
		UploadID: upload.UploadID,
	})
}

func (s *Server) handleUploadPart(ctx context.Context, w http.ResponseWriter, r *http.Request, bucket, key string, q url.Values) {
	partNum, err := strconv.Atoi(q.Get("partNumber"))
	if err != nil || partNum < 1 || partNum > 10000 {
		writeError(w, r, s3Error{Code: "InvalidArgument", Message: "Invalid part number", Resource: resourcePath(bucket, key), StatusCode: http.StatusBadRequest})
		return
	}
	uploadID := q.Get("uploadId")
	if !s.validateMultipartRoute(ctx, w, r, routeInfo{bucket: bucket, key: key}, uploadID) {
		return
	}

	if err := enforceContentLength(r); err != nil {
		writeError(w, r, s3Error{Code: "InvalidArgument", Message: err.Error(), Resource: resourcePath(bucket, key), StatusCode: http.StatusBadRequest})
		return
	}
	if err := verifyContentMD5(r); err != nil {
		code := "InvalidArgument"
		if errors.Is(err, storage.ErrMD5Mismatch) {
			code = "BadDigest"
		}
		writeError(w, r, s3Error{Code: code, Message: err.Error(), Resource: resourcePath(bucket, key), StatusCode: http.StatusBadRequest})
		return
	}
	checksumAlgorithm, checksumValue, err := checksumFromRequest(r)
	if err != nil {
		code := "InvalidArgument"
		if errors.Is(err, storage.ErrChecksumMismatch) {
			code = "BadDigest"
		}
		writeError(w, r, s3Error{Code: code, Message: err.Error(), Resource: resourcePath(bucket, key), StatusCode: http.StatusBadRequest})
		return
	}
	part, err := s.store.UploadPart(ctx, uploadID, partNum, r.Body)
	if err != nil {
		writeError(w, r, mapStorageError(err, resourcePath(bucket, key)))
		return
	}
	w.Header().Set("ETag", part.ETag)
	if checksumAlgorithm != "" {
		w.Header().Set("x-amz-checksum-"+strings.ToLower(checksumAlgorithm), checksumValue)
	}
	writeXML(w, r, http.StatusOK, uploadPartResult{ETag: part.ETag})
}

func (s *Server) handleCompleteMultipartUpload(ctx context.Context, w http.ResponseWriter, r *http.Request, bucket, key, uploadID string) {
	if !s.validateMultipartRoute(ctx, w, r, routeInfo{bucket: bucket, key: key}, uploadID) {
		return
	}
	var req completeMultipartUploadRequest
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, s3Error{Code: "MalformedXML", Message: "Malformed XML", Resource: resourcePath(bucket, key), StatusCode: http.StatusBadRequest})
		return
	}
	parts := make([]storage.PartInfo, 0, len(req.Parts))
	for _, p := range req.Parts {
		etag := strings.Trim(p.ETag, "\"")
		parts = append(parts, storage.PartInfo{PartNumber: p.PartNumber, ETag: "\"" + etag + "\""})
	}

	// Validate minimum part size for non-final parts
	storedParts, _ := s.store.ListParts(ctx, uploadID)
	partSizes := map[int]int64{}
	for _, p := range storedParts {
		partSizes[p.PartNumber] = p.Size
	}
	maxPart := 0
	for _, p := range parts {
		if p.PartNumber > maxPart {
			maxPart = p.PartNumber
		}
	}
	for _, p := range parts {
		if p.PartNumber < maxPart {
			if sz, ok := partSizes[p.PartNumber]; ok && sz < minPartSize {
				writeError(w, r, s3Error{Code: "EntityTooSmall", Message: "Your proposed upload is smaller than the minimum allowed size", Resource: resourcePath(bucket, key), StatusCode: http.StatusBadRequest})
				return
			}
		}
	}

	meta, err := s.store.CompleteMultipartUpload(ctx, uploadID, parts)
	if err != nil {
		writeError(w, r, mapStorageError(err, resourcePath(bucket, key)))
		return
	}
	writeXML(w, r, http.StatusOK, completeMultipartUploadResult{
		Bucket:   bucket,
		Key:      key,
		ETag:     meta.ETag,
		Location: "/" + bucket + "/" + key,
	})
}

func (s *Server) handleAbortMultipartUpload(ctx context.Context, w http.ResponseWriter, r *http.Request, bucket, key, uploadID string) {
	if !s.validateMultipartRoute(ctx, w, r, routeInfo{bucket: bucket, key: key}, uploadID) {
		return
	}
	err := s.store.AbortMultipartUpload(ctx, uploadID)
	if err != nil {
		writeError(w, r, mapStorageError(err, resourcePath(bucket, key)))
		return
	}
	w.Header().Set("x-amz-request-id", requestIDFromContext(ctx))
	setCORS(w, r)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListMultipartUploads(ctx context.Context, w http.ResponseWriter, r *http.Request, bucket string, q url.Values) {
	maxUploads := 1000
	if raw := q.Get("max-uploads"); raw != "" {
		if value, err := strconv.Atoi(raw); err == nil && value > 0 {
			maxUploads = value
		}
	}
	result, err := s.store.ListMultipartUploads(ctx, bucket, storage.MultipartListOptions{
		Prefix:         q.Get("prefix"),
		Delimiter:      q.Get("delimiter"),
		KeyMarker:      q.Get("key-marker"),
		UploadIDMarker: q.Get("upload-id-marker"),
		MaxUploads:     maxUploads,
	})
	if err != nil {
		writeError(w, r, mapStorageError(err, "/"+bucket))
		return
	}
	uploads := make([]multipartUploadXML, 0, len(result.Uploads))
	for _, upload := range result.Uploads {
		uploads = append(uploads, multipartUploadXML{
			Key:       upload.Key,
			UploadID:  upload.UploadID,
			Initiated: formatTime(upload.Initiated),
		})
	}
	writeXML(w, r, http.StatusOK, listMultipartUploadsResult{
		Xmlns:              xmlNS,
		Bucket:             bucket,
		KeyMarker:          result.KeyMarker,
		UploadIDMarker:     result.UploadIDMarker,
		NextKeyMarker:      result.NextKeyMarker,
		NextUploadIDMarker: result.NextUploadIDMarker,
		Prefix:             result.Prefix,
		Delimiter:          result.Delimiter,
		MaxUploads:         result.MaxUploads,
		IsTruncated:        result.IsTruncated,
		Uploads:            uploads,
	})
}

func (s *Server) handleListParts(ctx context.Context, w http.ResponseWriter, r *http.Request, bucket, key, uploadID string) {
	if !s.validateMultipartRoute(ctx, w, r, routeInfo{bucket: bucket, key: key}, uploadID) {
		return
	}
	parts, err := s.store.ListParts(ctx, uploadID)
	if err != nil {
		writeError(w, r, mapStorageError(err, resourcePath(bucket, key)))
		return
	}
	entries := make([]partEntry, 0, len(parts))
	for _, p := range parts {
		entries = append(entries, partEntry{
			PartNumber:   p.PartNumber,
			LastModified: formatTime(p.LastModified),
			ETag:         p.ETag,
			Size:         p.Size,
		})
	}
	writeXML(w, r, http.StatusOK, listPartsResult{
		Bucket:   bucket,
		Key:      key,
		UploadID: uploadID,
		MaxParts: 1000,
		Parts:    entries,
	})
}

func (s *Server) serveRange(w http.ResponseWriter, r *http.Request, rc io.ReadCloser, meta *storage.ObjectMeta, rangeHdr, bucket, key string) {
	defer rc.Close()
	start, end, err := parseRange(rangeHdr, meta.Size)
	if err != nil {
		w.Header().Set("Content-Range", "bytes */"+strconv.FormatInt(meta.Size, 10))
		writeError(w, r, s3Error{Code: "InvalidRange", Message: "The requested range is not satisfiable", Resource: resourcePath(bucket, key), StatusCode: http.StatusRequestedRangeNotSatisfiable})
		return
	}
	length := end - start + 1

	if seeker, ok := rc.(io.ReadSeeker); ok {
		_, _ = seeker.Seek(start, io.SeekStart)
	} else {
		_, _ = io.CopyN(io.Discard, rc, start)
	}

	setObjectHeaders(w, meta)
	w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
	w.Header().Set("Content-Range", "bytes "+strconv.FormatInt(start, 10)+"-"+strconv.FormatInt(end, 10)+"/"+strconv.FormatInt(meta.Size, 10))
	setCORS(w, r)
	w.Header().Set("x-amz-request-id", requestIDFromContext(r.Context()))
	w.WriteHeader(http.StatusPartialContent)
	_, _ = io.CopyN(w, rc, length)
}

func parseRange(hdr string, size int64) (start, end int64, err error) {
	if !strings.HasPrefix(hdr, "bytes=") {
		return 0, 0, errInvalidRange
	}
	spec := strings.TrimPrefix(hdr, "bytes=")
	if strings.HasPrefix(spec, "-") {
		// suffix range: last N bytes
		n, err := strconv.ParseInt(spec[1:], 10, 64)
		if err != nil || n <= 0 {
			return 0, 0, errInvalidRange
		}
		if n > size {
			n = size
		}
		return size - n, size - 1, nil
	}
	parts := strings.SplitN(spec, "-", 2)
	s, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, 0, errInvalidRange
	}
	var e int64
	if parts[1] == "" {
		e = size - 1
	} else {
		e, err = strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			return 0, 0, errInvalidRange
		}
	}
	if s < 0 || s >= size || e < s || e >= size {
		return 0, 0, errInvalidRange
	}
	return s, e, nil
}

var errInvalidRange = &rangeError{}

type rangeError struct{}

func (e *rangeError) Error() string { return "invalid range" }
