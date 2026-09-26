// Package authority answers one question: is a named operation permitted?
//
// It exists because authorization was happening at the wrong layer. SigV4
// authenticates a caller to the S3 interface, which means anything reaching a
// store by another route — the embedded API in pkg/stow, the WASM runtime, the
// workspace filesystem — had no authorization at all. That is not a bypass to
// be closed later; there was nothing there to bypass.
//
// The rule this package is built to make true:
//
//	Allowed(op) == Environment.Authority.Allows(op)
//
// enforced once, below every interface, so S3 and native cannot disagree about
// what a caller may do. An adapter may narrow authority by choosing a smaller
// value; it may never widen it.
package authority

import (
	"fmt"
	"sort"
)

// Operation is a single permission. The set is deliberately small and closed:
// every entry corresponds to something a caller can observe or change, so an
// unlisted action is denied rather than allowed by omission.
type Operation string

const (
	// Object data.
	ObjectRead   Operation = "object.read"
	ObjectWrite  Operation = "object.write"
	ObjectDelete Operation = "object.delete"
	ObjectList   Operation = "object.list"

	// The namespace itself. A bucket is a name in a namespace, so creating and
	// deleting one is not an object operation and does not inherit its
	// permissions.
	BucketCreate Operation = "bucket.create"
	BucketDelete Operation = "bucket.delete"
	BucketList   Operation = "bucket.list"

	// Lifecycle of the whole environment.
	EnvironmentReset   Operation = "environment.reset"
	EnvironmentDestroy Operation = "environment.destroy"
	EnvironmentPromote Operation = "environment.promote"

	// Reaching a related object store. Separate from the local permissions on
	// purpose: a caller that can write locally has not thereby been granted
	// authority to change someone else's data.
	UpstreamRead  Operation = "upstream.read"
	UpstreamWrite Operation = "upstream.write"
)

// All is every operation. It is the zero value's meaning on purpose: an
// Instance opened without an explicit Authority behaves as it always has, and
// the default documents today's behaviour instead of quietly narrowing it.
// Tightening the default is a deliberate, separate change.
//
// Mask is exported so the runtime can carry the value in a comparable field
// and so attenuation can be a subtraction rather than a set comparison.
type Authority struct {
	Mask uint32
}

// All permits every operation.
func All() Authority { return Authority{Mask: all} }

// None permits nothing.
func None() Authority { return Authority{0} }

// Allows reports whether op is permitted.
//
// The test is a single mask intersection because this runs on every operation
// of every request. A map lookup here would make authorization the most
// expensive line in the hot path, which is how it gets skipped in a hurry.
func (a Authority) Allows(op Operation) bool {
	bit, ok := operationBits[op]
	if !ok {
		// An operation nobody defined is not granted by default. A typo in a
		// caller's operation list must fail closed, not open.
		return false
	}
	return a.Mask&bit == bit
}

// With returns a copy that additionally permits the given operations.
func (a Authority) With(ops ...Operation) Authority {
	mask := a.Mask
	for _, op := range ops {
		if bit, ok := operationBits[op]; ok {
			mask |= bit
		}
	}
	return Authority{Mask: mask}
}

// Without returns a copy that no longer permits the given operations.
func (a Authority) Without(ops ...Operation) Authority {
	mask := a.Mask
	for _, op := range ops {
		if bit, ok := operationBits[op]; ok {
			mask &^= bit
		}
	}
	return Authority{Mask: mask}
}

// IsSupersetOf reports whether a permits everything b permits.
//
// This is the attenuation rule: a derived capability may be weaker than the one
// it came from, never stronger. It is defined here rather than in the
// derivation code so the rule has one implementation and one test.
func (a Authority) IsSupersetOf(b Authority) bool {
	return a.Mask&b.Mask == b.Mask
}

// Operations lists what is permitted, sorted, for a descriptor or a diagnostic.
// Sorted so the output is stable: an unordered list makes a capability payload
// differ between two runs of the same program.
func (a Authority) Operations() []Operation {
	out := make([]Operation, 0, len(operationBits))
	for op := range operationBits {
		if a.Allows(op) {
			out = append(out, op)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// String renders the permitted set, for an error message or a log line.
func (a Authority) String() string {
	if a.Mask == 0 {
		return "none"
	}
	if a.Mask == all {
		return "all"
	}
	ops := a.Operations()
	out := make([]byte, 0, len(ops)*12)
	for i, op := range ops {
		if i > 0 {
			out = append(out, ',', ' ')
		}
		out = append(out, op...)
	}
	return string(out)
}

// ErrNotAuthorized is returned for a refused operation. It is deliberately not
// an S3 error code: which code an interface renders it as is that interface's
// business, and the whole point of enforcing here is that the answer does not
// depend on which interface asked.
type ErrNotAuthorized struct {
	Operation Operation
	Authority Authority
}

func (e *ErrNotAuthorized) Error() string {
	return fmt.Sprintf("operation %q is not permitted by this environment (granted: %s)",
		e.Operation, e.Authority)
}

// Check returns *ErrNotAuthorized when op is not permitted. Callers use it as
// `if err := i.check(op); err != nil { return err }` at the top of each
// operation, which keeps the refusal and the operation name adjacent.
func (a Authority) Check(op Operation) error {
	if a.Allows(op) {
		return nil
	}
	return &ErrNotAuthorized{Operation: op, Authority: a}
}

var operationBits = func() map[Operation]uint32 {
	ops := []Operation{
		ObjectRead, ObjectWrite, ObjectDelete, ObjectList,
		BucketCreate, BucketDelete, BucketList,
		EnvironmentReset, EnvironmentDestroy, EnvironmentPromote,
		UpstreamRead, UpstreamWrite,
	}
	m := make(map[Operation]uint32, len(ops))
	for i, op := range ops {
		m[op] = 1 << uint(i)
	}
	return m
}()

// all is every bit in the set, derived from the list above rather than written
// out, so adding an Operation cannot leave All() quietly narrower than the set
// of things there are permissions for.
var all = func() (mask uint32) {
	for _, bit := range operationBits {
		mask |= bit
	}
	return mask
}()

// OperationBit exposes the bit for an Operation, for callers that need to
// reason about sets directly rather than call All and IsSupersetOf.
func OperationBit(op Operation) (uint32, bool) {
	bit, ok := operationBits[op]
	return bit, ok
}

// Defined lists every operation this package knows, for a caller that wants to
// assert it has covered the set.
func Defined() []Operation {
	out := make([]Operation, 0, len(operationBits))
	for op := range operationBits {
		out = append(out, op)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
