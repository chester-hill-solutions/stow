package storage

import (
	"time"
)

// ObjectMeta describes a stored object.
type ObjectMeta struct {
	Bucket            string
	Key               string
	Size              int64
	ETag              string
	ContentType       string
	LastModified      time.Time
	Metadata          map[string]string
	ChecksumAlgorithm string
	ChecksumValue     string
}

// BucketInfo describes a bucket.
type BucketInfo struct {
	Name         string
	CreationDate time.Time
}

// PutOptions configures object writes.
type PutOptions struct {
	ContentType       string
	Metadata          map[string]string
	ChecksumAlgorithm string
	ChecksumValue     string
	IfMatch           string
	IfNoneMatch       string
}

// ListOptions configures object listing.
type ListOptions struct {
	Prefix            string
	Delimiter         string
	ContinuationToken string
	MaxKeys           int
	StartAfter        string
}

// ListResult is the result of ListObjectsV2.
type ListResult struct {
	Objects               []ObjectMeta
	CommonPrefixes        []string
	IsTruncated           bool
	ContinuationToken     string
	NextContinuationToken string
	KeyCount              int
}

// MultipartUpload describes an in-progress multipart upload.
type MultipartUpload struct {
	UploadID  string
	Bucket    string
	Key       string
	Initiated time.Time
}

type MultipartListOptions struct {
	Prefix         string
	Delimiter      string
	KeyMarker      string
	UploadIDMarker string
	MaxUploads     int
}

type MultipartListResult struct {
	Uploads            []MultipartUpload
	Prefix             string
	Delimiter          string
	KeyMarker          string
	UploadIDMarker     string
	NextKeyMarker      string
	NextUploadIDMarker string
	MaxUploads         int
	IsTruncated        bool
}

// PartInfo describes a single uploaded part.
type PartInfo struct {
	PartNumber   int
	ETag         string
	Size         int64
	LastModified time.Time
}
