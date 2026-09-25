package fs

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	storage "github.com/chester-hill-solutions/stow-s3/internal/storage"
)

const (
	storeLockName       = ".stow.lock"
	storeLockStaleAfter = 10 * time.Minute
)

func acquireStoreLock(lockPath string) (string, error) {
	for attempt := 0; attempt < 2; attempt++ {
		nonce, err := storage.NewUploadID()
		if err != nil {
			return "", err
		}
		lockID := fmt.Sprintf("%d:%s", os.Getpid(), nonce)
		lock, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			if _, writeErr := io.WriteString(lock, lockID+"\n"); writeErr != nil {
				_ = lock.Close()
				_ = os.Remove(lockPath)
				return "", fmt.Errorf("write store lock: %w", writeErr)
			}
			if closeErr := lock.Close(); closeErr != nil {
				_ = os.Remove(lockPath)
				return "", closeErr
			}
			return lockID, nil
		}
		if !os.IsExist(err) {
			return "", fmt.Errorf("acquire store lock: %w", err)
		}
		if !isStaleStoreLock(lockPath) {
			return "", fmt.Errorf("acquire store lock: %w", err)
		}
		if removeErr := os.Remove(lockPath); removeErr != nil && !os.IsNotExist(removeErr) {
			return "", fmt.Errorf("recover stale store lock: %w", removeErr)
		}
	}
	return "", fmt.Errorf("acquire store lock: %w", os.ErrExist)
}

func isStaleStoreLock(lockPath string) bool {
	info, err := os.Stat(lockPath)
	if err != nil {
		return os.IsNotExist(err)
	}
	if !info.Mode().IsRegular() {
		return false
	}

	data, err := os.ReadFile(lockPath)
	if err != nil {
		return time.Since(info.ModTime()) >= storeLockStaleAfter
	}
	fields := strings.Fields(string(data))
	if len(fields) > 0 {
		pidText, _, ok := strings.Cut(fields[0], ":")
		if ok {
			pid, parseErr := strconv.Atoi(pidText)
			if parseErr == nil && pid > 0 {
				// A live owner wins over the age heuristic, even for a long-running store.
				return !storeLockProcessAlive(pid)
			}
		}
	}
	// Locks written by older versions have no owner metadata. Recover those
	// only after they have been idle long enough to be safely considered stale.
	return time.Since(info.ModTime()) >= storeLockStaleAfter
}

func storeLockProcessAlive(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = process.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}
	return !errors.Is(err, os.ErrProcessDone) && !errors.Is(err, syscall.ESRCH)
}

func releaseStoreLock(lockPath, lockID string) error {
	if lockPath == "" || lockID == "" {
		return nil
	}
	data, err := os.ReadFile(lockPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(data)) != lockID {
		return nil
	}
	if err := os.Remove(lockPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
