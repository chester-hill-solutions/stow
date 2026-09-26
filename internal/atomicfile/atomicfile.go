// Package atomicfile writes a file so that a reader sees either the old contents
// or the new ones, and so that a write which reports success survives a crash.
//
// Two functions in this repository had this shape and offered different
// guarantees. The filesystem backend synced the parent directory after rename and
// discarded both the open and the sync error, so a failed sync reported success;
// the workspace manifest renamed with no directory sync at all, which is not
// durable until the directory entry is synced. One implementation means one
// guarantee, and the guarantee includes the errors.
package atomicfile

import (
	"fmt"
	"os"
	"path/filepath"
)

// Write replaces path with data, atomically and durably.
//
// The sequence is temp file, write, fsync the file, close, rename, fsync the
// parent directory. The last step is the one that is easy to leave out and the one
// that decides whether the rename itself survives a power loss: the file's data
// can be on disk while the directory entry naming it is not.
//
// Every error is returned. A caller that cannot be told the write did not happen
// cannot decide what to do about it, and the common response — retry, or carry on
// — is wrong in both directions.
//
// The parent directory is created if missing, so callers do not each have to.
func Write(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return wrap(path, err)
	}

	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return wrap(path, err)
	}
	tmpName := tmp.Name()
	// Until the rename succeeds the temporary file is litter. Deferred rather
	// than repeated at each failure so a new error path cannot forget it.
	renamed := false
	defer func() {
		if !renamed {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return wrap(path, err)
	}
	// The permissions are set before the sync: the mode is part of what has to
	// reach the disk, not a detail to apply afterwards.
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return wrap(path, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return wrap(path, err)
	}
	if err := tmp.Close(); err != nil {
		return wrap(path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return wrap(path, err)
	}
	renamed = true
	if err := syncDir(dir); err != nil {
		return wrap(path, err)
	}
	return nil
}

// wrap names the file in a failure. A caller holding many paths cannot otherwise
// tell which write failed, and "not a directory" on its own is not a bug report.
func wrap(path string, err error) error {
	return fmt.Errorf("atomicfile: write %s: %w", path, err)
}

// syncDir fsyncs a directory so that a rename into it is durable.
//
// Both errors are returned. A directory that cannot be opened, or whose sync
// fails, means the rename may not survive a crash — and reporting success there
// is how "the write was acknowledged" stops meaning anything.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("atomicfile: open parent directory %s: %w", dir, err)
	}
	syncErr := d.Sync()
	closeErr := d.Close()
	if syncErr != nil {
		return fmt.Errorf("atomicfile: sync parent directory %s: %w", dir, syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("atomicfile: close parent directory %s: %w", dir, closeErr)
	}
	return nil
}
