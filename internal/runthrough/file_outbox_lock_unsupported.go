//go:build aix || js || plan9 || solaris || wasip1

package runthrough

import "errors"

// These targets do not expose the advisory file-lock primitive used by the
// outbox. Keep the package buildable without pretending that a process-local
// no-op provides cross-process coordination.
type outboxFileLock struct{}

func acquireOutboxFileLock(string) (*outboxFileLock, error) {
	return nil, errors.ErrUnsupported
}

func (*outboxFileLock) release() error {
	return errors.ErrUnsupported
}
