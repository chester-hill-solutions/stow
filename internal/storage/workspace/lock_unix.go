//go:build unix

package workspace

import (
	"errors"
	"os"
	"syscall"
)

// tryLock takes an exclusive advisory lock on f without blocking, and reports
// whether it succeeded. A failure that is not contention is returned as an
// error rather than being reported as "somebody else holds it", because those
// two mean opposite things to a collector.
func tryLock(f *os.File) (bool, error) {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, syscall.EWOULDBLOCK), errors.Is(err, syscall.EAGAIN):
		return false, nil
	default:
		return false, err
	}
}

// unlockFile releases the advisory lock. Closing the file would release it too,
// but doing it explicitly keeps the intent visible at the call site.
func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}

// lockSupported reports whether this host can establish session liveness at all.
const lockSupported = true
