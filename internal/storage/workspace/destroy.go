package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrNotDestructible is returned when a caller asks to remove a workspace stow
// will not delete: one it adopted rather than created, or one whose path is a
// directory the caller would not want gone.
//
// It is a refusal, not a failure. The workspace is untouched and still usable.
var ErrNotDestructible = errors.New("workspace is not one stow created")

// Destroy removes a workspace directory stow created, and nothing else.
//
// This is the operation ADR 0009 requires to be explicit, and the reason the
// manifest records ownership at all: a workspace is *designed* to be pointed at
// a directory the caller already has, so "this directory contains a stow
// manifest" is not evidence that its contents belong to stow. Adopting a
// project and then deleting it would be the worst bug this package could have.
//
// Two layers have to agree, and they are deliberately redundant. Ownership is
// the primary defence, because it is the only statement about who made the
// directory. The protected-path check is the backstop, and it is the layer that
// still holds when a workspace was created inside a directory that later became
// a developer's home or working directory, or when a caller guesses a path
// another stow process once used.
func (s *Store) Destroy() error {
	if err := s.assertDestructible(); err != nil {
		return err
	}
	if err := s.Close(); err != nil {
		return fmt.Errorf("workspace store: close before destroy: %w", err)
	}
	if err := os.RemoveAll(s.root); err != nil {
		return fmt.Errorf("workspace store: destroy: %w", err)
	}
	return nil
}

// assertDestructible applies both defences and explains a refusal in terms the
// caller can act on, rather than returning a bare sentinel.
func (s *Store) assertDestructible() error {
	if !s.manifest.Owned {
		return fmt.Errorf("%w: %s already existed, so stow adopted it. "+
			"Remove it yourself if that is what you want", ErrNotDestructible, s.root)
	}
	if reason, protected := protectedReason(s.root); protected {
		return fmt.Errorf("%w: %s is %s. Pass a subdirectory stow created.",
			ErrNotDestructible, s.root, reason)
	}
	return nil
}

// protectedReason reports whether a path is one whose deletion would destroy a
// developer's own work even when a workspace legitimately lives there.
//
// The working directory check is deliberately against the *process* working
// directory, and the home check against the user's home. Neither is a
// theoretical concern: `dir: ".."` resolves to a path that is not literally the
// home directory but lands on one just the same.
func protectedReason(path string) (string, bool) {
	target := resolvePath(path)
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if isAncestorOrSelf(target, resolvePath(home)) {
			return "your home directory or an ancestor of it", true
		}
	}
	if cwd, err := os.Getwd(); err == nil {
		if isAncestorOrSelf(target, resolvePath(cwd)) {
			return "the current working directory or an ancestor of it", true
		}
	}
	return "", false
}

// isAncestorOrSelf reports whether candidate is an ancestor of, or equal to,
// protected.
func isAncestorOrSelf(candidate, protected string) bool {
	if candidate == protected {
		return true
	}
	return strings.HasPrefix(protected, candidate+string(filepath.Separator))
}

// resolvePath makes a path absolute and clean, so the comparison above is not
// defeated by `..`, a trailing separator, or a relative path.
func resolvePath(path string) string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return filepath.Clean(absolute)
}

// directoryExists reports whether path is already a directory.
func directoryExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
