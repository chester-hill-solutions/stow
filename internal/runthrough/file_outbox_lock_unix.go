//go:build !windows

package runthrough

import (
	"os"
	"syscall"
)

type outboxFileLock struct {
	file *os.File
}

func acquireOutboxFileLock(path string) (*outboxFileLock, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		_ = file.Close()
		return nil, err
	}
	return &outboxFileLock{file: file}, nil
}

func (lock *outboxFileLock) release() error {
	unlockErr := syscall.Flock(int(lock.file.Fd()), syscall.LOCK_UN)
	closeErr := lock.file.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}
