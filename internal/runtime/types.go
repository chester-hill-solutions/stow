package runtime

import (
	"errors"
	"fmt"
	"time"

	"github.com/chester-hill-solutions/stow-s3/internal/authority"
)

type Backend string

const (
	BackendMemory     Backend = "memory"
	BackendFilesystem Backend = "filesystem"
	// BackendWorkspace is a directory a caller is working in, whose objects are
	// real files. Like the filesystem backend it is persistent, and like the
	// filesystem backend it requires a store to be supplied by the caller: a
	// workspace needs a directory, which Options does not carry. It is reached
	// through stow.OpenWorkspace rather than through Open, so there is no way to
	// ask for a workspace without saying where it is.
	BackendWorkspace Backend = "workspace"

	DefaultMaxBytes   int64 = 64 << 20
	DefaultMaxObjects int64 = 10_000

	// UnlimitedBytes and UnlimitedObjects disable quota enforcement. The
	// embedded profile applies DefaultMaxBytes/DefaultMaxObjects; a long-lived
	// native server keeps its existing unbounded behavior unless an operator
	// opts in, and a scoped agent session passes explicit limits.
	UnlimitedBytes   int64 = 1<<63 - 1
	UnlimitedObjects int64 = 1<<63 - 1
)

var (
	ErrClosed                   = errors.New("runtime is closed")
	ErrQuotaExceeded            = errors.New("runtime quota exceeded")
	ErrUnsupportedBackend       = errors.New("unsupported runtime backend")
	ErrInvalidListLimit         = errors.New("runtime list limit must not be negative")
	ErrExternalResetUnsupported = errors.New("runtime reset is unsupported for an externally managed store")
	ErrMultipartUnsupported     = errors.New("runtime multipart operations are unsupported")
)

// isKnownBackend reports whether the runtime understands a backend name. An
// unrecognised one is refused rather than treated as memory, because a caller
// who asked for durable bytes must not be handed volatile ones.
func isKnownBackend(backend Backend) bool {
	switch backend {
	case BackendMemory, BackendFilesystem, BackendWorkspace:
		return true
	default:
		return false
	}
}

// IsPersistentBackend reports whether a backend keeps objects across a reopen.
// It is the capability a host asks about, and it is why the two persistent
// backends need a store supplied rather than constructed from Options.
//
// Exported because a host that reports capabilities has to answer the same
// question the runtime does. A host that answers it with its own string
// comparison is the reason this is exported rather than kept internal: the
// readiness payload used to claim `backend == "filesystem"`, which happened to
// agree for the two backends the CLI could select and silently disagreed about
// the third.
func IsPersistentBackend(backend Backend) bool {
	return backend == BackendFilesystem || backend == BackendWorkspace
}

// normalizeOptions validates a backend choice and applies the quota defaults.
// Memory needs no store; the two persistent backends do, which is why they are
// reachable only from OpenWithStore. See isKnownBackend in types.go.
func normalizeOptions(options Options, boundStore bool) (Options, error) {
	if options.Backend == "" {
		options.Backend = BackendMemory
	}
	if boundStore {
		if !isKnownBackend(options.Backend) {
			return Options{}, ErrUnsupportedBackend
		}
	} else if options.Backend != BackendMemory {
		return Options{}, ErrUnsupportedBackend
	}
	if options.MaxBytes < 0 || options.MaxObjects < 0 {
		return Options{}, fmt.Errorf("runtime quotas must not be negative")
	}
	if options.MaxBytes == 0 {
		options.MaxBytes = DefaultMaxBytes
	}
	if options.MaxObjects == 0 {
		options.MaxObjects = DefaultMaxObjects
	}
	return options, nil
}

type Options struct {
	Backend    Backend
	MaxBytes   int64
	MaxObjects int64

	// Authority is what this environment permits. It is enforced here, below
	// every interface, so that S3 and a native caller cannot be granted
	// different things.
	//
	// DisableMultipart reports that the bound store cannot serve multipart. It is
	// opt-out rather than a positive flag because every store this repository
	// ships can, and a caller who says nothing is describing a working one.
	//
	// It exists because the alternative was a capability that was decided by which
	// constructor was called rather than by the store: OpenWithStore passed true
	// unconditionally, so a caller-supplied store that implemented no multipart at
	// all was advertised as supporting it and failed partway through an upload
	// instead. The store is the thing that knows the answer.
	DisableMultipart bool

	// A nil pointer means every operation, and that is the default on purpose:
	// it is what an Instance opened without one has always permitted, so adding
	// the field changes no existing behaviour. Pass &authority.None() to permit
	// nothing. The pointer is what makes those two states distinguishable — a
	// bare Authority cannot tell "unset" from "explicitly empty".
	Authority *authority.Authority
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
	Bucket            string
	Key               string
	Data              []byte
	Size              int64
	ETag              string
	VersionID         string
	ContentType       string
	Metadata          map[string]string
	LastModified      time.Time
	ChecksumAlgorithm string
	ChecksumValue     string
}

type PutOptions struct {
	ContentType       string
	Metadata          map[string]string
	ChecksumAlgorithm string
	ChecksumValue     string
	IfMatch           string
	IfNoneMatch       string
}

type ObjectPage struct {
	Objects        []Object
	CommonPrefixes []string
	Truncated      bool
	Cursor         string
	NextCursor     string
	KeyCount       int
}

type ListOptions struct {
	Prefix     string
	Cursor     string
	Limit      int
	Delimiter  string
	StartAfter string
}
