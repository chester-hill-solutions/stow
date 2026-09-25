package runthrough

import (
	"sort"
	"sync"
)

type outboxKeyLock struct {
	mu   sync.Mutex
	refs int
}

type outboxKeyLocks struct {
	mu    sync.Mutex
	locks map[string]*outboxKeyLock
}

func (s *outboxKeyLocks) lock(key string) func() {
	s.mu.Lock()
	if s.locks == nil {
		s.locks = make(map[string]*outboxKeyLock)
	}
	lock := s.locks[key]
	if lock == nil {
		lock = &outboxKeyLock{}
		s.locks[key] = lock
	}
	lock.refs++
	s.mu.Unlock()

	lock.mu.Lock()
	return func() {
		lock.mu.Unlock()
		s.mu.Lock()
		lock.refs--
		if lock.refs == 0 && s.locks[key] == lock {
			delete(s.locks, key)
		}
		s.mu.Unlock()
	}
}

func (s *outboxKeyLocks) lockMany(keys []string) func() {
	if len(keys) == 0 {
		return func() {}
	}
	unique := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		unique[key] = struct{}{}
	}
	ordered := make([]string, 0, len(unique))
	for key := range unique {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)

	unlocks := make([]func(), 0, len(ordered))
	for _, key := range ordered {
		unlocks = append(unlocks, s.lock(key))
	}
	return func() {
		for i := len(unlocks) - 1; i >= 0; i-- {
			unlocks[i]()
		}
	}
}

func outboxIdentity(bucket, key string) string {
	return bucket + "\x00" + key
}
