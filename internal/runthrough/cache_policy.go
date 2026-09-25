package runthrough

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/chester-hill-solutions/stow-s3/internal/storage"
)

type cacheEntry struct {
	accessedAt time.Time
	expiresAt  time.Time
}

type cacheCandidate struct {
	key  string
	meta storage.ObjectMeta
	at   time.Time
}

func (a *Adapter) CacheEvictions() uint64 {
	return a.cacheEvictions.Load()
}

func (a *Adapter) touchCache(bucket, key string) {
	if !a.separateCache {
		return
	}
	a.cacheMu.Lock()
	defer a.cacheMu.Unlock()
	now := time.Now()
	accessKey := cacheEntryKey(bucket, key)
	entry := a.cacheEntries[accessKey]
	entry.accessedAt = now
	if entry.expiresAt.IsZero() && a.cfg.Cache.TTL > 0 {
		entry.expiresAt = now.Add(a.cfg.Cache.TTL)
	}
	a.cacheEntries[accessKey] = entry
}

func (a *Adapter) trackCacheObject(ctx context.Context, bucket, key string) error {
	if !a.separateCache {
		return nil
	}
	a.cacheMu.Lock()
	delete(a.cacheEntries, cacheEntryKey(bucket, key))
	a.cacheMu.Unlock()
	a.touchCache(bucket, key)
	return a.evictCache(ctx)
}

func (a *Adapter) cacheEntryExpired(bucket, key string) bool {
	if a.cfg.Cache.TTL <= 0 {
		return false
	}
	a.cacheMu.Lock()
	defer a.cacheMu.Unlock()
	entry, ok := a.cacheEntries[cacheEntryKey(bucket, key)]
	return ok && !entry.expiresAt.IsZero() && !time.Now().Before(entry.expiresAt)
}

func (a *Adapter) evictCache(ctx context.Context) error {
	policy := a.cfg.Cache
	if policy.MaxBytes <= 0 && policy.MaxObjects <= 0 && policy.TTL <= 0 {
		return nil
	}
	candidates, totalBytes, err := a.collectCacheCandidates(ctx)
	if err != nil {
		return err
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].at.Equal(candidates[j].at) {
			return candidates[i].key < candidates[j].key
		}
		return candidates[i].at.Before(candidates[j].at)
	})
	for len(candidates) > 0 && cacheOverBudget(len(candidates), totalBytes, policy) {
		victim := candidates[0]
		candidates = candidates[1:]
		if err := a.cache.DeleteObject(ctx, victim.meta.Bucket, victim.meta.Key); err != nil && !storageErrIsMissingObject(err) {
			return err
		}
		totalBytes -= victim.meta.Size
		a.cacheMu.Lock()
		delete(a.cacheEntries, victim.key)
		a.cacheMu.Unlock()
		a.cacheEvictions.Add(1)
	}
	return nil
}

func (a *Adapter) collectCacheCandidates(ctx context.Context) ([]cacheCandidate, int64, error) {
	buckets, err := a.cache.ListBuckets(ctx)
	if err != nil {
		return nil, 0, err
	}
	var candidates []cacheCandidate
	var totalBytes int64
	now := time.Now()
	for _, bucket := range buckets {
		objects, listErr := listAllObjects(ctx, a.cache, bucket.Name, "")
		if listErr != nil {
			if storageErrIsMissingBucket(listErr) {
				continue
			}
			return nil, 0, listErr
		}
		for _, object := range objects {
			accessKey := cacheEntryKey(bucket.Name, object.Key)
			a.cacheMu.Lock()
			entry, ok := a.cacheEntries[accessKey]
			a.cacheMu.Unlock()
			accessed := object.LastModified
			if ok {
				if !entry.expiresAt.IsZero() && !now.Before(entry.expiresAt) {
					if err := a.cache.DeleteObject(ctx, bucket.Name, object.Key); err != nil && !storageErrIsMissingObject(err) {
						return nil, 0, err
					}
					a.cacheMu.Lock()
					delete(a.cacheEntries, accessKey)
					a.cacheMu.Unlock()
					a.cacheEvictions.Add(1)
					continue
				}
				accessed = entry.accessedAt
			}
			candidates = append(candidates, cacheCandidate{key: accessKey, meta: object, at: accessed})
			totalBytes += object.Size
		}
	}
	return candidates, totalBytes, nil
}

func cacheOverBudget(objects int, bytes int64, policy CachePolicy) bool {
	return policy.MaxObjects > 0 && int64(objects) > policy.MaxObjects || policy.MaxBytes > 0 && bytes > policy.MaxBytes
}

func cacheEntryKey(bucket, key string) string {
	return bucket + "\x00" + key
}

func storageErrIsMissingBucket(err error) bool {
	return errors.Is(err, storage.ErrBucketNotFound)
}

func storageErrIsMissingObject(err error) bool {
	return errors.Is(err, storage.ErrObjectNotFound)
}
