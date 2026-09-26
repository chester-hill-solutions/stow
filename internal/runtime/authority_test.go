package runtime_test

import (
	"context"
	"errors"
	"testing"

	"github.com/chester-hill-solutions/stow-s3/internal/authority"
	"github.com/chester-hill-solutions/stow-s3/internal/runtime"
	"github.com/chester-hill-solutions/stow-s3/internal/storage"
)

// openWith builds an environment with the given Authority over a store that
// already holds a bucket.
//
// The bucket is seeded through the store rather than through the Instance,
// because a restricted authority correctly refuses to create one — and because
// an environment's contents come from somewhere else, not from the permissions
// of whoever is holding it.
func openWith(t *testing.T, granted authority.Authority) *runtime.Instance {
	t.Helper()
	store := storage.NewMemoryStore()
	if err := store.CreateBucket(context.Background(), "bucket"); err != nil {
		t.Fatalf("seed bucket: %v", err)
	}
	instance, err := runtime.OpenWithStore(runtime.Options{
		Backend:   runtime.BackendMemory,
		Authority: &granted,
	}, store, nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() {
		if err := instance.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	return instance
}

func denied(t *testing.T, err error) bool {
	t.Helper()
	var refused *authority.ErrNotAuthorized
	return errors.As(err, &refused)
}

// The default must not have changed. An Instance opened without an Authority
// behaves exactly as it did before the field existed, which is what makes adding
// it a non-breaking change rather than a silent tightening.
func TestAnUnsetAuthorityPermitsEverything(t *testing.T) {
	instance, err := runtime.OpenWithStore(
		runtime.Options{Backend: runtime.BackendMemory},
		storage.NewMemoryStore(), nil,
	)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer instance.Close()

	if !instance.Authority().IsSupersetOf(authority.All()) {
		t.Fatal("an unset Authority must permit every operation")
	}
	for _, op := range authority.Defined() {
		if err := instance.Authority().Check(op); err != nil {
			t.Errorf("unset Authority refused %q", op)
		}
	}
}

// An explicitly empty Authority is distinguishable from an unset one. Without
// the pointer there would be no way to say "permit nothing", which is the state
// an environment starts in before a caller is granted anything.
func TestAnExplicitlyEmptyAuthorityPermitsNothing(t *testing.T) {
	instance := openWith(t, authority.None())
	if instance.Authority().Allows(authority.ObjectRead) {
		t.Fatal("None must permit nothing")
	}
	if err := instance.CreateBucket(context.Background(), "another"); !denied(t, err) {
		t.Fatalf("CreateBucket under None = %v, want *ErrNotAuthorized", err)
	}
}

func TestReadOnlyRefusesEveryMutation(t *testing.T) {
	ctx := context.Background()
	instance := openWith(t, authority.All().Without(
		authority.ObjectWrite, authority.ObjectDelete,
		authority.BucketCreate, authority.BucketDelete,
		authority.EnvironmentReset,
	))

	if _, err := instance.PutObject(ctx, "bucket", "k", []byte("x"), runtime.PutOptions{}); !denied(t, err) {
		t.Errorf("PutObject = %v, want a refusal", err)
	}
	if err := instance.DeleteObject(ctx, "bucket", "k"); !denied(t, err) {
		t.Errorf("DeleteObject = %v, want a refusal", err)
	}
	if err := instance.DeleteBucket(ctx, "bucket"); !denied(t, err) {
		t.Errorf("DeleteBucket = %v, want a refusal", err)
	}
	if err := instance.CreateBucket(ctx, "fresh"); !denied(t, err) {
		t.Errorf("CreateBucket = %v, want a refusal", err)
	}
	if err := instance.Reset(ctx); !denied(t, err) {
		t.Errorf("Reset = %v, want a refusal", err)
	}

	// Reads still work, or the environment is useless rather than restricted.
	if _, err := instance.ListBuckets(ctx); err != nil {
		t.Errorf("ListBuckets = %v, want success", err)
	}
	if _, err := instance.ListObjects(ctx, "bucket", runtime.ListOptions{}); err != nil {
		t.Errorf("ListObjects = %v, want success", err)
	}
}

// Multipart is a write in several steps. A read-only environment must not be
// able to start an upload, add a part to one, or complete it.
func TestReadOnlyRefusesMultipartWrites(t *testing.T) {
	ctx := context.Background()
	instance := openWith(t, authority.All().Without(authority.ObjectWrite))

	upload, err := instance.CreateMultipartUpload(ctx, "bucket", "big")
	if !denied(t, err) {
		t.Fatalf("CreateMultipartUpload = %v, want a refusal", err)
	}
	if upload != nil {
		t.Fatal("a refused upload must not hand back an upload id")
	}
}

// A refused operation must not leave anything behind. This is the difference
// between a permission check and a check that happens after the work.
func TestARefusedWriteStoresNothing(t *testing.T) {
	ctx := context.Background()
	instance := openWith(t, authority.All().Without(authority.ObjectWrite))

	if _, err := instance.PutObject(ctx, "bucket", "k", []byte("x"), runtime.PutOptions{}); !denied(t, err) {
		t.Fatalf("PutObject = %v, want a refusal", err)
	}
	if _, err := instance.HeadObject(ctx, "bucket", "k"); !errors.Is(err, storage.ErrObjectNotFound) {
		t.Errorf("a refused put left an object: %v", err)
	}
	if _, err := instance.CreateMultipartUpload(ctx, "bucket", "big"); !denied(t, err) {
		t.Errorf("CreateMultipartUpload = %v, want a refusal", err)
	}
	if uploads, err := instance.ListMultipartUploads(ctx, "bucket", storage.MultipartListOptions{}); err != nil {
		t.Errorf("list uploads: %v", err)
	} else if len(uploads.Uploads) != 0 {
		t.Errorf("a refused upload left %d uploads behind", len(uploads.Uploads))
	}
}

// The whole point of enforcing below the interfaces: the store the S3 server is
// given is an adapter over this Instance, so an S3 caller and a native caller
// cannot be granted different things. If this test needs the adapter to grow its
// own check, the enforcement has leaked upward.
func TestTheStoreAdapterInheritsTheSameRefusals(t *testing.T) {
	ctx := context.Background()
	instance := openWith(t, authority.All().Without(authority.ObjectWrite))

	adapter, err := runtime.NewStoreAdapter(instance)
	if err != nil {
		t.Fatalf("adapter: %v", err)
	}

	// This is the call the S3 handler makes.
	_, err = adapter.PutObject(ctx, "bucket", "k", nil, storage.PutOptions{})
	if !denied(t, err) {
		t.Fatalf("adapter PutObject = %v, want the same refusal the native path gave", err)
	}
	// And the reads still work through the same door.
	if _, err := adapter.ListBuckets(ctx); err != nil {
		t.Errorf("adapter ListBuckets = %v, want success", err)
	}
}

// Close is never gated. A caller that cannot dispose of a handle leaks a
// process, a temporary directory and any in-flight multipart state, which is a
// worse outcome than a caller tidying up after itself.
func TestDisposalIsAlwaysPermitted(t *testing.T) {
	granted := authority.None()
	instance, err := runtime.OpenWithStore(runtime.Options{
		Backend:   runtime.BackendMemory,
		Authority: &granted,
	}, storage.NewMemoryStore(), nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := instance.Close(); err != nil {
		t.Fatalf("Close under an empty Authority = %v, want success", err)
	}
}

// A derived capability may be weaker and never stronger. The rule is tested
// here against a real Instance because that is where it will be relied on when
// handoff and promotion arrive.
func TestANarrowerAuthorityCanOpenItsOwnEnvironment(t *testing.T) {
	parent := authority.All().With(authority.UpstreamRead)
	child := parent.Without(authority.UpstreamRead, authority.ObjectDelete)

	if !parent.IsSupersetOf(child) {
		t.Fatal("a derived Authority must be a subset of the one it came from")
	}

	instance := openWith(t, child)
	if instance.Authority().Allows(authority.UpstreamRead) {
		t.Error("the child must not have kept the authority the parent gave up")
	}
	if !instance.Authority().Allows(authority.ObjectWrite) {
		t.Error("the child must keep what the parent kept")
	}
}
