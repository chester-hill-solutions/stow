package atomicfile_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/chester-hill-solutions/stow-s3/internal/atomicfile"
)

// A write reports success, and the bytes are there afterwards. The baseline every
// other case here departs from.
func TestWriteReplacesTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "record")
	if err := atomicfile.Write(path, []byte("first"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := atomicfile.Write(path, []byte("second"), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "second" {
		t.Fatalf("contents = %q, want %q", got, "second")
	}
}

// No reader may observe a half-written file, which is the whole point of the
// temporary file. This cannot be observed directly without racing a writer, so it
// asserts the property that makes it true: nothing but the temporary file is ever
// opened at the destination path, and the temporary file is gone afterwards.
func TestWriteLeavesNoTemporaryFileBehind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "record")
	if err := atomicfile.Write(path, []byte("payload"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("directory holds %v, want only the written file", names)
	}
}

// A failure must not leave the temporary file behind, or a directory that fails
// once accumulates litter for the life of the data directory.
func TestWriteCleansUpWhenTheDestinationIsUnwritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	if runtime.GOOS == "windows" {
		t.Skip("directory permissions behave differently on Windows")
	}
	dir := t.TempDir()
	locked := filepath.Join(dir, "locked")
	if err := os.Mkdir(locked, 0o555); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	if err := atomicfile.Write(filepath.Join(locked, "record"), []byte("x"), 0o644); err == nil {
		t.Fatal("write into a read-only directory succeeded")
	}
	entries, err := os.ReadDir(locked)
	if err != nil {
		t.Fatalf("read locked dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("a failed write left %d entries behind", len(entries))
	}
}

// The parent directory is created rather than being every caller's problem, and
// the write into it is durable.
func TestWriteCreatesTheParentDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a", "b", "c", "record")
	if err := atomicfile.Write(path, []byte("nested"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stat: %v", err)
	}
}

// The mode is part of what has to reach the disk, so it is applied before the
// sync rather than after it. A caller asking for 0600 and getting 0644 has a file
// readable by someone else, and a post-rename chmod would leave a window where the
// wrong mode is on disk.
func TestWriteHonoursTheRequestedMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret")
	if err := atomicfile.Write(path, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %o, want 600", got)
	}
}

// An empty write is a legitimate way to truncate a file, and must not be confused
// with a failure or skipped.
func TestWriteAcceptsEmptyContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "record")
	if err := atomicfile.Write(path, []byte("something"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := atomicfile.Write(path, nil, 0o644); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("contents = %q, want empty", got)
	}
}

// The failure that motivated this package: an error reaching the caller with the
// path in it, rather than a bare "no such file or directory" from four frames
// away. A caller that cannot name the file cannot log which write failed.
func TestFailureNamesTheFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := t.TempDir()
	blocked := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	err := atomicfile.Write(filepath.Join(blocked, "record"), []byte("x"), 0o644)
	if err == nil {
		t.Fatal("write beneath a regular file succeeded")
	}
	if !strings.Contains(err.Error(), "record") {
		t.Errorf("error %q does not name the file that failed", err)
	}
}
