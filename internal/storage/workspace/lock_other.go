//go:build !unix

package workspace

import "os"

// tryLock cannot establish liveness here, so it never claims a lock.
func tryLock(_ *os.File) (bool, error) {
	return false, ErrLockUnsupported
}

func unlockFile(_ *os.File) error { return ErrLockUnsupported }

// lockSupported is false, and the collector reports it rather than silently
// skipping workspaces it cannot reason about.
const lockSupported = false
