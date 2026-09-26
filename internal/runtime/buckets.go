package runtime

import (
	"context"

	"github.com/chester-hill-solutions/stow-s3/internal/authority"
)

// The bucket half of the namespace. A bucket is a name in it, so these
// operations are separate from the object ones rather than sharing their
// permissions: authority to write an object is not authority to create or
// remove the container it lives in.

func (i *Instance) HeadBucket(ctx context.Context, name string) (Bucket, error) {
	if err := i.checkContext(ctx); err != nil {
		return Bucket{}, err
	}
	if err := i.check(authority.BucketList); err != nil {
		return Bucket{}, err
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if err := i.checkOpen(); err != nil {
		return Bucket{}, err
	}
	info, err := i.store.HeadBucket(ctx, name)
	if err != nil {
		return Bucket{}, err
	}
	return Bucket{Name: info.Name, CreationDate: info.CreationDate}, nil
}

func (i *Instance) CreateBucket(ctx context.Context, name string) error {
	if err := i.checkContext(ctx); err != nil {
		return err
	}
	if err := i.check(authority.BucketCreate); err != nil {
		return err
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if err := i.checkOpen(); err != nil {
		return err
	}
	return i.store.CreateBucket(ctx, name)
}

func (i *Instance) DeleteBucket(ctx context.Context, name string) error {
	if err := i.checkContext(ctx); err != nil {
		return err
	}
	if err := i.check(authority.BucketDelete); err != nil {
		return err
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if err := i.checkOpen(); err != nil {
		return err
	}
	return i.store.DeleteBucket(ctx, name)
}

func (i *Instance) ListBuckets(ctx context.Context) ([]Bucket, error) {
	if err := i.checkContext(ctx); err != nil {
		return nil, err
	}
	if err := i.check(authority.BucketList); err != nil {
		return nil, err
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if err := i.checkOpen(); err != nil {
		return nil, err
	}
	items, err := i.store.ListBuckets(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Bucket, 0, len(items))
	for _, item := range items {
		out = append(out, Bucket{Name: item.Name, CreationDate: item.CreationDate})
	}
	return out, nil
}
