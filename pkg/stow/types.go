package stow

import "github.com/chester-hill-solutions/stow/internal/runtime"

type Backend = runtime.Backend
type Options = runtime.Options
type Capabilities = runtime.Capabilities
type Usage = runtime.Usage
type Bucket = runtime.Bucket
type Object = runtime.Object
type ObjectPage = runtime.ObjectPage
type PutOptions = runtime.PutOptions
type ListOptions = runtime.ListOptions

const (
	BackendMemory = runtime.BackendMemory
)

var (
	ErrClosed             = runtime.ErrClosed
	ErrQuotaExceeded      = runtime.ErrQuotaExceeded
	ErrUnsupportedBackend = runtime.ErrUnsupportedBackend
	ErrInvalidListLimit   = runtime.ErrInvalidListLimit
)
