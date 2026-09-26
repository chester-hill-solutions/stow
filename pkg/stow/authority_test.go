package stow_test

import (
	"context"
	"errors"
	"testing"

	"github.com/chester-hill-solutions/stow-s3/pkg/stow"
)

// The embedded surface is the one that had no authorization at all, so this is
// the test that would have failed before the field existed. It uses only the
// public package: an embedder should not have to reach into internal paths to
// say what a capability permits.
func TestEmbeddedRuntimeRefusesWhatItsAuthorityDoesNotGrant(t *testing.T) {
	readOnly := stow.ReadOnly()
	runtime, err := stow.Open(stow.Options{Authority: &readOnly})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer runtime.Close()

	ctx := context.Background()
	// A bucket that does not exist, so this also pins the ordering: the
	// authority is consulted before the store is, and a refusal is a refusal
	// rather than a NoSuchBucket the caller could act on.
	_, err = runtime.PutObject(ctx, "no-such-bucket", "key", []byte("payload"), stow.PutOptions{})
	var refused *stow.ErrNotAuthorized
	if !errors.As(err, &refused) {
		t.Fatalf("PutObject = %v (%T), want *stow.ErrNotAuthorized", err, err)
	}
	if refused.Operation != stow.ObjectWrite {
		t.Errorf("refused %q, want object.write", refused.Operation)
	}
	if _, err := runtime.ListBuckets(ctx); err != nil {
		t.Errorf("ListBuckets = %v, want success", err)
	}
	if err := runtime.CreateBucket(ctx, "bucket"); err == nil {
		t.Error("CreateBucket succeeded under ReadOnly")
	}
}

// An unset Authority must leave the embedded runtime exactly as permissive as
// it was, or adding the field is a silent breaking change.
func TestAnEmbeddedRuntimeWithoutAnAuthorityIsUnrestricted(t *testing.T) {
	runtime, err := stow.Open(stow.Options{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer runtime.Close()

	ctx := context.Background()
	if err := runtime.CreateBucket(ctx, "bucket"); err != nil {
		t.Fatalf("CreateBucket = %v, want success", err)
	}
	if _, err := runtime.PutObject(ctx, "bucket", "key", []byte("payload"), stow.PutOptions{}); err != nil {
		t.Fatalf("PutObject = %v, want success", err)
	}
}

// ReadWrite deliberately withholds the authority to reach outside the
// environment or tear it down. A caller that can write locally has not thereby
// been authorized to change someone else's data.
func TestReadWriteWithholdsUpstreamAndDisposal(t *testing.T) {
	granted := stow.ReadWrite()
	for _, op := range []stow.Operation{
		stow.UpstreamRead, stow.UpstreamWrite,
		stow.EnvironmentDestroy, stow.EnvironmentPromote,
	} {
		if granted.Allows(op) {
			t.Errorf("ReadWrite() permits %q", op)
		}
	}
	for _, op := range []stow.Operation{
		stow.ObjectRead, stow.ObjectWrite, stow.ObjectDelete, stow.ObjectList,
		stow.BucketCreate, stow.BucketDelete, stow.BucketList,
	} {
		if !granted.Allows(op) {
			t.Errorf("ReadWrite() refuses %q", op)
		}
	}
	if !granted.IsSupersetOf(stow.ReadOnly()) {
		t.Error("ReadWrite must be able to do everything ReadOnly can")
	}
}

// A derived capability may be weaker and never stronger. The vocabulary is
// public, so the rule is testable by an embedder rather than only here.
func TestADerivedAuthorityIsWeakerAndNeverStronger(t *testing.T) {
	parent := stow.AllowAll()
	child := parent.Without(stow.ObjectWrite, stow.UpstreamWrite)

	if !parent.IsSupersetOf(child) {
		t.Error("the child must be a subset of the parent")
	}
	if child.IsSupersetOf(parent) {
		t.Error("a child must never be a superset of its parent")
	}
	if !parent.Allows(stow.ObjectWrite) {
		t.Error("deriving a child must not narrow the parent")
	}
}
