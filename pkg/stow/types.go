package stow

import (
	"errors"
	"time"
)

type Backend string

const BackendMemory Backend = "memory"

type Options struct {
	Backend    Backend
	MaxBytes   int64
	MaxObjects int64
}

type Capabilities struct {
	Backend    Backend
	MaxBytes   int64
	MaxObjects int64
	Persistent bool
	Multipart  bool
	Upstream   bool
}

type Usage struct {
	Bytes   int64
	Objects int64
}

type Bucket struct {
	Name         string
	CreationDate time.Time
}

type Object struct {
	Bucket       string
	Key          string
	Data         []byte
	Size         int64
	ETag         string
	ContentType  string
	Metadata     map[string]string
	LastModified time.Time
}

type ObjectPage struct {
	Objects    []Object
	Truncated  bool
	NextCursor string
}

type PutOptions struct {
	ContentType string
	Metadata    map[string]string
}

type ListOptions struct {
	Prefix string
	Cursor string
	Limit  int
}

var (
	ErrClosed             = errors.New("stow: runtime is closed")
	ErrQuotaExceeded      = errors.New("stow: runtime quota exceeded")
	ErrUnsupportedBackend = errors.New("stow: unsupported runtime backend")
	ErrInvalidListLimit   = errors.New("stow: list limit must not be negative")
)
