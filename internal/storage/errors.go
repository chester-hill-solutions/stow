package storage

import "errors"

var (
	ErrBucketNotFound     = errors.New("bucket not found")
	ErrInvalidBucketName  = errors.New("invalid bucket name")
	ErrBucketExists       = errors.New("bucket already exists")
	ErrObjectNotFound     = errors.New("object not found")
	ErrInvalidKey         = errors.New("invalid object key")
	ErrInvalidUpload      = errors.New("invalid multipart upload")
	ErrInvalidPart        = errors.New("invalid multipart part")
	ErrUploadNotFound     = errors.New("multipart upload not found")
	ErrNoSuchUpload       = errors.New("no such upload")
	ErrPreconditionFailed = errors.New("precondition failed")
	ErrBucketNotEmpty     = errors.New("bucket not empty")
)
