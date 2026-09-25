package stow

import "errors"

var (
	ErrBucketNotFound = errors.New("stow: bucket not found")
	ErrBucketExists   = errors.New("stow: bucket already exists")
	ErrBucketNotEmpty = errors.New("stow: bucket is not empty")
	ErrObjectNotFound = errors.New("stow: object not found")
	ErrInvalidBucket  = errors.New("stow: invalid bucket name")
	ErrInvalidKey     = errors.New("stow: invalid object key")
)
