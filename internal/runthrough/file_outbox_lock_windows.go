//go:build windows

package runthrough

import (
	"os"
	"syscall"
	"unsafe"
)

const (
	lockFileExclusiveLock = 0x00000002
	lockFileReserved      = 0
	lockFileBytes         = ^uint32(0)
)

var (
	kernel32     = syscall.NewLazyDLL("kernel32.dll")
	lockFileEx   = kernel32.NewProc("LockFileEx")
	unlockFileEx = kernel32.NewProc("UnlockFileEx")
)

type outboxFileLock struct {
	file       *os.File
	overlapped syscall.Overlapped
}

func acquireOutboxFileLock(path string) (*outboxFileLock, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}

	lock := &outboxFileLock{file: file}
	result, _, err := lockFileEx.Call(
		file.Fd(),
		uintptr(lockFileExclusiveLock),
		uintptr(lockFileReserved),
		uintptr(lockFileBytes),
		uintptr(lockFileBytes),
		uintptr(unsafe.Pointer(&lock.overlapped)),
	)
	if result == 0 {
		_ = file.Close()
		if err == nil {
			err = syscall.EINVAL
		}
		return nil, err
	}
	return lock, nil
}

func (lock *outboxFileLock) release() error {
	result, _, unlockErr := unlockFileEx.Call(
		lock.file.Fd(),
		uintptr(lockFileReserved),
		uintptr(lockFileBytes),
		uintptr(lockFileBytes),
		uintptr(unsafe.Pointer(&lock.overlapped)),
	)
	closeErr := lock.file.Close()
	if result == 0 {
		return unlockErr
	}
	return closeErr
}
