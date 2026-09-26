package stow_test

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chester-hill-solutions/stow-s3/pkg/stow"
)

// memoryStore is a caller-supplied stow.Store written against the public
// interface only — no internal package, which is the whole point of the
// interface existing.
type memoryStore struct {
	mu      sync.Mutex
	buckets map[string]time.Time
	objects map[string]map[string]stow.Object
	closed  bool
}

func newMemoryStore() *memoryStore {
	return &memoryStore{
		buckets: map[string]time.Time{},
		objects: map[string]map[string]stow.Object{},
	}
}

func (m *memoryStore) CreateBucket(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return errors.New("closed")
	}
	if _, ok := m.buckets[name]; ok {
		return stow.ErrBucketExists
	}
	m.buckets[name] = time.Now()
	m.objects[name] = map[string]stow.Object{}
	return nil
}

func (m *memoryStore) DeleteBucket(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.buckets[name]; !ok {
		return stow.ErrBucketNotFound
	}
	delete(m.buckets, name)
	delete(m.objects, name)
	return nil
}

func (m *memoryStore) ListBuckets(context.Context) ([]stow.Bucket, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]stow.Bucket, 0, len(m.buckets))
	for name, created := range m.buckets {
		out = append(out, stow.Bucket{Name: name, CreationDate: created})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m *memoryStore) PutObject(_ context.Context, bucket, key string, data []byte, options stow.PutOptions) (stow.Object, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.buckets[bucket]; !ok {
		return stow.Object{}, errors.New("no such bucket")
	}
	metadata := map[string]string{}
	for k, v := range options.Metadata {
		metadata[k] = v
	}
	object := stow.Object{
		Bucket:       bucket,
		Key:          key,
		Data:         append([]byte(nil), data...),
		Size:         int64(len(data)),
		ContentType:  options.ContentType,
		Metadata:     metadata,
		ETag:         "supplied",
		LastModified: time.Now().UTC(),
	}
	m.objects[bucket][key] = object
	return object, nil
}

func (m *memoryStore) GetObject(ctx context.Context, bucket, key string) (stow.Object, error) {
	object, err := m.HeadObject(ctx, bucket, key)
	if err != nil {
		return stow.Object{}, err
	}
	return object, nil
}

func (m *memoryStore) HeadObject(_ context.Context, bucket, key string) (stow.Object, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	objects, ok := m.objects[bucket]
	if !ok {
		return stow.Object{}, stow.ErrBucketNotFound
	}
	object, ok := objects[key]
	if !ok {
		return stow.Object{}, stow.ErrObjectNotFound
	}
	return object, nil
}

func (m *memoryStore) DeleteObject(_ context.Context, bucket, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	objects, ok := m.objects[bucket]
	if !ok {
		return stow.ErrBucketNotFound
	}
	if _, ok := objects[key]; !ok {
		return stow.ErrObjectNotFound
	}
	delete(objects, key)
	return nil
}

func (m *memoryStore) ListObjects(_ context.Context, bucket string, options stow.ListOptions) (stow.ObjectPage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	objects, ok := m.objects[bucket]
	if !ok {
		return stow.ObjectPage{}, stow.ErrBucketNotFound
	}
	page := stow.ObjectPage{}
	for key, object := range objects {
		if options.Prefix != "" && !strings.HasPrefix(key, options.Prefix) {
			continue
		}
		page.Objects = append(page.Objects, object)
	}
	sort.Slice(page.Objects, func(i, j int) bool { return page.Objects[i].Key < page.Objects[j].Key })
	if options.Limit > 0 && len(page.Objects) > options.Limit {
		page.Objects = page.Objects[:options.Limit]
		page.Truncated = true
	}
	return page, nil
}

func (m *memoryStore) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}

// A caller-supplied store is the thing the architecture said would be a one-line
// change and was not possible at all. This is that case.
func TestOpenAcceptsACallerSuppliedStore(t *testing.T) {
	ctx := context.Background()
	store := newMemoryStore()

	runtime, err := stow.Open(stow.Options{Store: store})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer runtime.Close()

	if err := runtime.CreateBucket(ctx, "bucket"); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	if _, err := runtime.PutObject(ctx, "bucket", "k", []byte("payload"), stow.PutOptions{
		ContentType: "text/plain",
	}); err != nil {
		t.Fatalf("PutObject: %v", err)
	}

	got, err := runtime.GetObject(ctx, "bucket", "k")
	if err != nil {
		t.Fatalf("GetObject: %v", err)
	}
	if string(got.Data) != "payload" {
		t.Errorf("data = %q, want %q", got.Data, "payload")
	}
	if got.ContentType != "text/plain" {
		t.Errorf("ContentType = %q, want text/plain", got.ContentType)
	}

	buckets, err := runtime.ListBuckets(ctx)
	if err != nil {
		t.Fatalf("ListBuckets: %v", err)
	}
	if len(buckets) != 1 || buckets[0].Name != "bucket" {
		t.Errorf("ListBuckets = %+v", buckets)
	}

	page, err := runtime.ListObjects(ctx, "bucket", stow.ListOptions{})
	if err != nil {
		t.Fatalf("ListObjects: %v", err)
	}
	if len(page.Objects) != 1 || page.Objects[0].Key != "k" {
		t.Errorf("ListObjects = %+v", page.Objects)
	}

	if err := runtime.DeleteObject(ctx, "bucket", "k"); err != nil {
		t.Fatalf("DeleteObject: %v", err)
	}
	if _, err := runtime.HeadObject(ctx, "bucket", "k"); err == nil {
		t.Error("HeadObject succeeded after a delete")
	}
}

// A prefix and a limit are options the internal contract carries and the public
// interface does not, so the adapter has to carry them across. A store that
// honours neither would be reported as listing everything, and a store that
// honours the prefix but not the limit would silently return too much.
func TestTheAdapterCarriesPrefixAndLimitAcross(t *testing.T) {
	ctx := context.Background()
	store := newMemoryStore()
	runtime, err := stow.Open(stow.Options{Store: store})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer runtime.Close()

	if err := runtime.CreateBucket(ctx, "bucket"); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	for _, key := range []string{"a/1", "a/2", "b/1"} {
		if _, err := runtime.PutObject(ctx, "bucket", key, []byte(key), stow.PutOptions{}); err != nil {
			t.Fatalf("PutObject(%q): %v", key, err)
		}
	}

	page, err := runtime.ListObjects(ctx, "bucket", stow.ListOptions{Prefix: "a/"})
	if err != nil {
		t.Fatalf("ListObjects with a prefix: %v", err)
	}
	if len(page.Objects) != 2 {
		t.Errorf("prefix a/ returned %d objects, want 2", len(page.Objects))
	}

	page, err = runtime.ListObjects(ctx, "bucket", stow.ListOptions{Limit: 1})
	if err != nil {
		t.Fatalf("ListObjects with a limit: %v", err)
	}
	if len(page.Objects) != 1 {
		t.Errorf("limit 1 returned %d objects, want 1", len(page.Objects))
	}
	if !page.Truncated {
		t.Error("a truncated page was not reported as truncated")
	}
}

// Multipart is an optional interface. A store that does not implement it must be
// ADVERTISED as not supporting it, not fail an upload partway through: the
// environment describing itself wrongly is the defect this fixes.
func TestAStoreWithoutMultipartIsAdvertisedAsNotSupportingIt(t *testing.T) {
	runtime, err := stow.Open(stow.Options{Store: newMemoryStore()})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer runtime.Close()

	if runtime.Capabilities().Multipart {
		t.Error("a store with no MultipartStore was advertised as supporting multipart")
	}
}

// And the other direction: a store that does implement it is advertised as
// supporting it. Getting this wrong the other way would refuse an upload a
// caller could have served.
func TestAStoreWithNoMultipartStillOpensWhenItAlreadyHasBuckets(t *testing.T) {
	ctx := context.Background()
	store := newMemoryStore()
	for _, bucket := range []string{"one", "two", "three"} {
		if err := store.CreateBucket(ctx, bucket); err != nil {
			t.Fatalf("seed %q: %v", bucket, err)
		}
	}

	runtime, err := stow.Open(stow.Options{Store: store})
	if err != nil {
		t.Fatalf("open over a populated store with no multipart: %v", err)
	}
	defer runtime.Close()

	buckets, err := runtime.ListBuckets(ctx)
	if err != nil {
		t.Fatalf("ListBuckets: %v", err)
	}
	if len(buckets) != 3 {
		t.Errorf("ListBuckets returned %d, want 3", len(buckets))
	}
}

func TestAuthorityStillAppliesOverASuppliedStore(t *testing.T) {
	ctx := context.Background()
	readOnly := stow.ReadOnly()
	store := newMemoryStore()
	// Seed through the store, because a read-only environment cannot create one.
	if err := store.CreateBucket(ctx, "bucket"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	runtime, err := stow.Open(stow.Options{Store: store, Authority: &readOnly})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer runtime.Close()

	if _, err := runtime.PutObject(ctx, "bucket", "k", []byte("x"), stow.PutOptions{}); err == nil {
		t.Error("PutObject succeeded under a read-only authority over a supplied store")
	}
	if _, err := runtime.ListBuckets(ctx); err != nil {
		t.Errorf("ListBuckets = %v, want success", err)
	}
}

// Closing the environment closes the supplied store, so a caller does not have to
// track two lifetimes. The runtime takes ownership: Close is documented as
// happening exactly once regardless of who owns it.
func TestClosingTheEnvironmentClosesTheSuppliedStore(t *testing.T) {
	store := newMemoryStore()
	runtime, err := stow.Open(stow.Options{Store: store})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	store.mu.Lock()
	closed := store.closed
	store.mu.Unlock()
	if !closed {
		t.Error("closing the environment did not close the supplied store")
	}
}

// A nil Store must remain the memory backend, or every existing caller changes.
func TestANilStoreIsStillTheMemoryBackend(t *testing.T) {
	runtime, err := stow.Open(stow.Options{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer runtime.Close()

	if got := runtime.Capabilities().Backend; got != stow.BackendMemory {
		t.Errorf("Backend = %q, want memory", got)
	}
	if runtime.Capabilities().Persistent {
		t.Error("the memory backend reported itself as persistent")
	}
}

// The point of the whole change: two environments, two stores, one constructor.
func TestDifferentStoresAreDifferentConfigurationsOfOneConstructor(t *testing.T) {
	ctx := context.Background()
	first := newMemoryStore()
	second := newMemoryStore()

	one, err := stow.Open(stow.Options{Store: first})
	if err != nil {
		t.Fatalf("open first: %v", err)
	}
	defer one.Close()
	two, err := stow.Open(stow.Options{Store: second})
	if err != nil {
		t.Fatalf("open second: %v", err)
	}
	defer two.Close()

	if err := one.CreateBucket(ctx, "bucket"); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	if _, err := one.PutObject(ctx, "bucket", "k", []byte("one"), stow.PutOptions{}); err != nil {
		t.Fatalf("PutObject: %v", err)
	}

	if _, err := two.ListBuckets(ctx); err != nil {
		t.Fatalf("second ListBuckets: %v", err)
	}
	buckets, err := two.ListBuckets(ctx)
	if err != nil {
		t.Fatalf("ListBuckets: %v", err)
	}
	if len(buckets) != 0 {
		t.Errorf("the second environment saw the first one's bucket: %+v", buckets)
	}
}
