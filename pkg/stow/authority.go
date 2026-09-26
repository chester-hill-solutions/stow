package stow

import "github.com/chester-hill-solutions/stow-s3/internal/authority"

// The authority vocabulary is re-exported rather than left behind an internal
// path. An embedder has to be able to say what a capability permits, and a
// public API that forces `internal/authority` to express that is not a public
// API.
type (
	// Operation is a single permission.
	Operation = authority.Operation
	// Authority is a set of permitted operations.
	Authority = authority.Authority
	// ErrNotAuthorized is returned for a refused operation.
	ErrNotAuthorized = authority.ErrNotAuthorized
)

const (
	ObjectRead   = authority.ObjectRead
	ObjectWrite  = authority.ObjectWrite
	ObjectDelete = authority.ObjectDelete
	ObjectList   = authority.ObjectList

	BucketCreate = authority.BucketCreate
	BucketDelete = authority.BucketDelete
	BucketList   = authority.BucketList

	EnvironmentReset   = authority.EnvironmentReset
	EnvironmentDestroy = authority.EnvironmentDestroy
	EnvironmentPromote = authority.EnvironmentPromote

	UpstreamRead  = authority.UpstreamRead
	UpstreamWrite = authority.UpstreamWrite
)

// AllowAll returns an Authority that permits every operation.
func AllowAll() Authority { return authority.All() }

// AllowNone returns an Authority that permits nothing.
func AllowNone() Authority { return authority.None() }

// ReadOnly returns an Authority that can read and list but change nothing.
//
// The presets are nested — ReadOnly is a subset of ReadWrite, which is a subset
// of AllowAll — so "at least this much" is expressible by picking one name.
func ReadOnly() Authority { return authority.ReadOnly() }

// ReadWrite returns an Authority for local work with no reach outside the
// environment.
func ReadWrite() Authority { return authority.ReadWrite() }
