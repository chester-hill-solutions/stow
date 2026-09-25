//go:build windows

package runthrough

import (
	"os"
	"time"
)

type outboxFileLock struct {
	file *os.File
	path string
}

func acquireOutboxFileLock(path string) (*outboxFileLock, error) {
	for attempt := 0; attempt < 10000; attempt++ {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		if err == nil {
			return &outboxFileLock{file: file, path: path}, nil
		}
		if !os.IsExist(err) {
			return nil, err
		}
		time.Sleep(time.Millisecond)
	}
	return nil, os.ErrExist
}

func (lock *outboxFileLock) release() error {
	closeErr := lock.file.Close()
	removeErr := os.Remove(lock.path)
	if closeErr != nil {
		return closeErr
	}
	return removeErr
}
