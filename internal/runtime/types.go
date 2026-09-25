package runtime

import (
	"errors"
	"time"
)

type Backend string

const (
	BackendMemory     Backend = "memory"
	BackendFilesystem Backend = "filesystem"

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
