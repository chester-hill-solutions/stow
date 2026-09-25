// Package workspace implements the storage backend behind ADR 0007 and ADR
// 0008: a store whose objects are real files in a real directory, which is what
// makes a workspace a working directory rather than a store with a friendlier
// name.
//
// The contract is docs/workspace-contract.md. This file implements its section
// 3, the key-to-path encoding, and is the part of the backend that has to be
// right on a case-insensitive filesystem.
package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
)

const (
	// internalDir holds everything stow owns inside a workspace: the manifest,
	// escaped keys, other buckets, and multipart staging. It is reserved as a
	// first path segment, so a user key can never be spelled this way and a
	// directory or file with this name at the root is unambiguously stow's.
	internalDir = ".stow"

	// escapeMarker may not appear in a segment of a natural key. A key
	// containing it is escaped, which keeps the two forms unambiguous by
	// inspection rather than by a stat. This is the same rule the record
	// store's shard prefix established: a layout that encodes structure in
	// names must make the names self-describing.
	escapeMarker = "~"

	// maxRelativePathBytes is a deliberately conservative cap on the whole
	// relative path. Windows MAX_PATH is 260 by default and long-path support
	// cannot be assumed, so a key that would exceed this is escaped rather than
	// written and failed later.
	maxRelativePathBytes = 240
)

// Layout maps object keys to paths inside one workspace directory. The zero
// value is usable; NewLayout returns one.
type Layout struct{}

// Form is how a key is represented on disk. Both are visible; neither is
// hidden behind an encoding a reader cannot inspect.
type Form string

const (
	// FormNatural is the key's own path: output/report.pdf at
	// <dir>/output/report.pdf.
	FormNatural Form = "natural"
	// FormEscaped is a digest under the internal directory, with the key
	// recorded in the manifest.
	FormEscaped Form = "escaped"
)

// NewLayout returns the layout for a workspace.
//
// The rules below are the *union* of what every supported host filesystem
// accepts, not an intersection of what this one happens to do. Three payoffs,
// and the third is the one that decided it:
//
//   - A key Windows cannot spell is escaped on Linux too, so a manifest written
//     on one host means the same thing on another. ADR 0009 section 6 scopes a
//     workspace to one machine, but "portable enough not to surprise" is
//     cheaper than an explicit non-portable rule nobody reads.
//   - The Windows rules become testable on Linux CI, which is where most
//     changes are tested. A host-conditional rule is an untested rule.
//   - It deletes a conditional: no windowsRules field, no runtime.GOOS branch,
//     and no second code path to keep in step with the first.
//
// The cost is that a handful of pathological keys lose their pretty path on
// Linux. Nobody's agent creates `what?.txt` on purpose.
func NewLayout() Layout {
	return Layout{}
}

// reservedDeviceNames are the names Windows refuses to use as a file name,
// with or without an extension, in any case.
var reservedDeviceNames = map[string]bool{
	"con": true, "prn": true, "aux": true, "nul": true,
	"com1": true, "com2": true, "com3": true, "com4": true, "com5": true,
	"com6": true, "com7": true, "com8": true, "com9": true,
	"lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true, "lpt5": true,
	"lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true,
}

// illegalWindowsChars are the characters Windows forbids in a file name. They
// are legal S3 key content, so a key using one is escaped rather than rejected:
// every key the S3 contract accepts has to be storable, or the workspace is not
// a drop-in for the store it replaces. They are rejected on every host, not
// just Windows; see NewLayout.
const illegalWindowsChars = `<>:"|?*`

// Digest is the escaped-form file name for a key: the lowercase hex SHA-256 of
// the key's UTF-8 bytes. The key itself is recorded in the manifest, so the
// digest only has to be collision-resistant, not reversible.
func Digest(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// splitKey breaks a key into path segments, reporting false when any segment
// cannot be a path component: empty (which is what a leading or doubled slash
// produces), the current directory, or the parent directory. Those keys are
// valid S3 content and are escaped rather than refused.
func splitKey(key string) ([]string, bool) {
	if key == "" {
		return nil, false
	}
	segments := strings.Split(key, "/")
	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." {
			return nil, false
		}
	}
	return segments, true
}

// IsNatural reports whether a key can be stored at its own path. It covers the
// conditions in docs/workspace-contract.md section 3.2 that do not depend on
// what else is in the workspace. The fifth condition, that no case-insensitively
// equal path is already taken, needs the store's index and is applied there.
func (l Layout) IsNatural(key string) bool {
	segments, ok := splitKey(key)
	if !ok {
		return false
	}
	if segments[0] == internalDir {
		return false
	}
	if len(key) > maxRelativePathBytes {
		return false
	}
	for _, segment := range segments {
		if !l.segmentAllowed(segment) {
			return false
		}
	}
	return true
}

// segmentAllowed reports whether one path segment is storable as-is.
//
// The Windows rules are applied on every host, deliberately; see NewLayout.
func (l Layout) segmentAllowed(segment string) bool {
	if strings.Contains(segment, escapeMarker) {
		return false
	}
	if !l.charsAllowed(segment) {
		return false
	}
	if strings.HasSuffix(segment, ".") || strings.HasSuffix(segment, " ") {
		return false
	}
	return !reservedDeviceName(segment)
}

// charsAllowed rejects control characters and NUL, which no filesystem holds,
// plus the characters Windows forbids.
func (l Layout) charsAllowed(segment string) bool {
	for _, r := range segment {
		if r == 0 || r < 0x20 {
			return false
		}
		if strings.ContainsRune(illegalWindowsChars, r) {
			return false
		}
	}
	return true
}

// reservedDeviceName reports whether a segment is a Windows device name, with
// or without an extension: NUL, nul.txt, and COM1 are all refused by the
// filesystem, and all three are legal S3 key content.
func reservedDeviceName(segment string) bool {
	name := segment
	if dot := strings.IndexByte(name, '.'); dot >= 0 {
		name = name[:dot]
	}
	return reservedDeviceNames[strings.ToLower(name)]
}

// FoldPath returns the case-folded form of a workspace-relative path, for
// comparing two paths the way a case-insensitive filesystem would.
//
// This is the whole of the collision defence from docs/workspace-contract.md
// section 3.4: Report.pdf and report.pdf are two S3 keys and one file on
// Windows or macOS, and serving both from one path loses one of them silently.
func FoldPath(relative string) string {
	return strings.ToLower(filepath.ToSlash(relative))
}

// NaturalPath returns the absolute path a key occupies when it is stored
// naturally, which is only meaningful when IsNatural reports true.
func NaturalPath(root, key string) string {
	return filepath.Join(append([]string{root}, strings.Split(key, "/")...)...)
}

// EscapedPath returns the absolute path a key occupies when it is stored
// escaped. The file name is a digest, so the path is within every filesystem's
// component limit for a key of any supported length.
func EscapedPath(root, key string) string {
	return filepath.Join(root, internalDir, "keys", Digest(key))
}

// InternalPath joins a path inside the reserved internal directory.
func InternalPath(root string, parts ...string) string {
	return filepath.Join(append([]string{root, internalDir}, parts...)...)
}

// IsInternal reports whether a workspace-relative path belongs to stow rather
// than to a key. Listing uses it to walk the tree without reporting stow's own
// bookkeeping as objects, and a key that would land there is escaped.
func IsInternal(relative string) bool {
	normalized := filepath.ToSlash(relative)
	return normalized == internalDir || strings.HasPrefix(normalized, internalDir+"/")
}

// KeyFromNaturalPath is the inverse of NaturalPath: it turns a
// workspace-relative path back into the key it holds. It reports false for a
// path inside the internal directory, which is not a key.
func KeyFromNaturalPath(relative string) (string, bool) {
	if IsInternal(relative) {
		return "", false
	}
	return filepath.ToSlash(relative), true
}
