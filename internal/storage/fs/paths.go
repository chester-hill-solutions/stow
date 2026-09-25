package fs

import (
	"encoding/hex"
	"strings"
)

// maxNameComponent is the portable limit on one filesystem path component. 255
// is NAME_MAX on ext4, APFS and NTFS, and it is the tightest of those, so a
// name that fits here fits on every platform the backend is supported on.
const maxNameComponent = 255

// shardChunk is how much of an encoded key goes into one shard directory. It
// sits below maxNameComponent so a prefixed shard name is still a legal path
// component and no shard needs escaping.
const shardChunk = 200

// shardPrefix marks a path component as a shard directory rather than an object
// record.
//
// The layout has to be unambiguous from the name alone, because a name is all a
// path gives you before you touch the disk. Record files and shard directories
// were originally both bare hex, which made them one name space: a key whose
// encoding fell exactly on a chunk boundary wrote its record to a path that a
// longer key sharing the same hex prefix needed to descend through, so writing
// the longer key failed with ENOTDIR while the shorter key sat in the way. Real
// keys share prefixes constantly, since hex preserves order, so this was not an
// exotic shape. A prefix that cannot occur in hex separates the two name spaces,
// which makes every component's role decidable from its form.
const shardPrefix = "_"

// objectRelPath returns a reversible, flat filesystem name for an object key.
// Encoding the complete key avoids path traversal and preserves empty, repeated,
// and dot path segments.
func objectRelPath(key string) string {
	return hex.EncodeToString([]byte(key))
}

// objectRelSegments returns an object's record path relative to the bucket's
// objects directory, as individual segments.
//
// Hex doubles a key's length in bytes, so a key longer than 127 bytes cannot fit
// in a single path component: it failed with ENAMETOOLONG even though
// storage.ValidateKey accepts up to 1024, and the caller saw a 500 for a key it
// was entitled to send. Those keys are split across shard directories, each
// holding one chunk of the same reversible encoding, so the joined name still
// decodes to exactly the original key.
//
// Every segment but the last is a shard directory and carries shardPrefix. The
// last is the record file and does not, so a path can always be classified into
// "descend" or "this is the object" without stat-ing anything.
//
// A key that fits stays flat. That keeps every existing data directory readable,
// because only long keys were ever affected and they were never successfully
// written, so no stored object depends on the sharded layout. It also means the
// prefix is only ever seen on keys that failed to write before this change, so
// introducing it cannot orphan a stored object.
func objectRelSegments(key string) []string {
	encoded := objectRelPath(key)
	if len(encoded) <= maxNameComponent {
		return []string{encoded}
	}
	segments := make([]string, 0, len(encoded)/shardChunk+1)
	for len(encoded) > shardChunk {
		segments = append(segments, shardPrefix+encoded[:shardChunk])
		encoded = encoded[shardChunk:]
	}
	return append(segments, encoded)
}

// isShardName reports whether a path component is a shard directory this package
// wrote. It is deliberately name-only, so callers can classify a path without a
// stat and so a directory the package did not create is never descended into or
// pruned.
func isShardName(name string) bool {
	if !strings.HasPrefix(name, shardPrefix) {
		return false
	}
	chunk := name[len(shardPrefix):]
	// A bare prefix, or an empty chunk, is not a name this package writes:
	// every chunk objectRelSegments produces is shardChunk characters long.
	// Rejecting them keeps the classifier exact, so a directory that happens to
	// be called "_" is never treated as a shard and pruned.
	if len(chunk) == 0 {
		return false
	}
	_, ok := objectKeyFromFilename(chunk)
	return ok
}

// objectKeyFromSegments reverses objectRelSegments for a path found by walking
// the objects directory, where the shard directories contribute the leading
// segments and the record file name is the last one.
func objectKeyFromSegments(segments []string) (string, bool) {
	if len(segments) == 0 {
		return "", false
	}
	encoded := make([]string, 0, len(segments))
	for i, segment := range segments {
		if i == len(segments)-1 {
			// The leaf is the record file and carries no prefix.
			encoded = append(encoded, segment)
			continue
		}
		if !isShardName(segment) {
			return "", false
		}
		encoded = append(encoded, segment[len(shardPrefix):])
	}
	return objectKeyFromFilename(strings.Join(encoded, ""))
}

func objectKeyFromFilename(name string) (string, bool) {
	decoded, err := hex.DecodeString(name)
	if err != nil {
		return "", false
	}
	return string(decoded), true
}
