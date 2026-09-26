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

	// Store is where the objects live. A nil Store means the memory backend,
	// which is what an embedded runtime has always done.
	//
	// Supplying one is what makes this composable: the architecture's claim that
	// a different store is a one-line change was false of the public API until
	// this field existed, because a filesystem or workspace runtime had to be
	// reached through a second constructor. Set Backend to match the store — a
	// Store on its own does not say whether it is durable, and the two answers
	// are not the same: a caller that asked for durable bytes must not be handed
	// volatile ones.
	Store Store

	// Authority is what this environment permits, enforced below every
	// interface so an embedded caller and an S3 client are granted the same
	// things. A nil pointer permits everything, which is what an embedded
	// runtime has always done.
	//
	// To start with nothing and grant as you go, take the address of a value:
	//
	//	readOnly := stow.ReadOnly()
	//	rt, err := stow.Open(stow.Options{Authority: &readOnly})
	Authority *Authority
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
