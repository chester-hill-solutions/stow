package runtime

import (
	"errors"
	"time"
)

type Backend string

const (
	BackendMemory Backend = "memory"

	DefaultMaxBytes   int64 = 64 << 20
	DefaultMaxObjects int64 = 10_000
)

var (
	ErrClosed             = errors.New("runtime is closed")
	ErrQuotaExceeded      = errors.New("runtime quota exceeded")
	ErrUnsupportedBackend = errors.New("unsupported runtime backend")
	ErrInvalidListLimit   = errors.New("runtime list limit must not be negative")
)

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

type PutOptions struct {
	ContentType string
	Metadata    map[string]string
}

type ObjectPage struct {
	Objects    []Object
	Truncated  bool
	NextCursor string
}

type ListOptions struct {
	Prefix string
	Cursor string
	Limit  int
}
