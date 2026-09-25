package stow

import (
	"errors"
	"time"
)

type Backend string

const (
	BackendMemory Backend = "memory"
	// BackendWorkspace is a directory a caller is working in, whose objects are
	// real files. It is reported by Workspace.Capabilities and is reachable only
	// through OpenWorkspace, never through Open: a workspace needs a directory,
	// and Open has nowhere to put one. Asking Open for it fails with
	// ErrUnsupportedBackend rather than quietly handing back a memory runtime.
	BackendWorkspace Backend = "workspace"
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
