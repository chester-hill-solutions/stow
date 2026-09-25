package workspace

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/chester-hill-solutions/stow-s3/internal/storage"
)

// ErrClosed is returned by every operation on a closed store. A workspace store
// is closed when its handle is released, not when its data is removed.
var ErrClosed = errors.New("workspace store is closed")

// locate returns the path a key currently occupies, preferring the form the
// manifest recorded and falling back to whichever file is actually there.
//
// The fallback is what makes adoption work: a file the host wrote has no
// manifest entry, so the only way to find it is to look.
func (s *Store) locate(bucket, key string) (string, error) {
	recorded, hasEntry := s.manifest.entry(bucket, key)
	if hasEntry && recorded.Form == FormEscaped {
		path := EscapedPath(s.root, key)
		if fileExists(path) {
			return path, nil
		}
	}

	if s.layout.IsNatural(key) {
		path := NaturalPath(s.bucketDir(bucket), key)
		if fileExists(path) {
			return path, nil
		}
	}

	escaped := EscapedPath(s.root, key)
	if fileExists(escaped) {
		return escaped, nil
	}
	return "", storage.ErrObjectNotFound
}

// resolve finds a key's file and its metadata, adopting the file when stow has
// no manifest entry for it.
//
// Adoption is not an import step. There is no separate registration pass,
// because a workspace whose files need registering before they can be read is
// not a working directory.
func (s *Store) resolve(bucket, key string) (string, os.FileInfo, ManifestEntry, error) {
	absPath, err := s.locate(bucket, key)
	if err != nil {
		return "", nil, ManifestEntry{}, err
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return "", nil, ManifestEntry{}, storage.ErrObjectNotFound
	}

	entry, recorded := s.manifest.entry(bucket, key)
	if !recorded || entry.stale(info.Size(), info.ModTime()) {
		entry = s.derive(absPath, info)
		entry.Form = s.formOf(bucket, key, absPath)
		if entry.Form == FormEscaped {
			entry.Digest = Digest(key)
		}
		// Reuse the recorded version so that re-reading an unchanged file keeps
		// reporting the same version rather than minting one per call.
		if prior, had := s.manifest.entry(bucket, key); had {
			entry.VersionID = prior.VersionID
		}
		if err := s.record(bucket, key, entry); err != nil {
			return "", nil, ManifestEntry{}, err
		}
	}
	return absPath, info, entry, nil
}

// formOf reports which form a path is in, by asking the layout where the key
// would go rather than by inspecting the path.
func (s *Store) formOf(bucket, key, absPath string) Form {
	natural := NaturalPath(s.bucketDir(bucket), key)
	if absPath == natural {
		return FormNatural
	}
	return FormEscaped
}

// derive builds a manifest entry for a file from the file itself: its size and
// modification time from the filesystem, its type from its first bytes, and its
// ETag from its content.
//
// A host-written file has no declared content type and no recorded ETag, and
// inventing either would be worse than deriving them. This is a full read of
// the file, once; the result is cached in the manifest and the read is not
// repeated while size and modification time still match.
func (s *Store) derive(absPath string, info os.FileInfo) ManifestEntry {
	entry := ManifestEntry{
		Size:        info.Size(),
		ContentType: detectContentType(absPath),
		Modified:    info.ModTime().UTC().Truncate(time.Second),
	}
	if data, err := os.ReadFile(absPath); err == nil {
		entry.ETag = storage.ETagForBytes(data)
	}
	entry.VersionID = entry.ETag
	return entry
}

// absorb records a derived entry so the next read does not pay for it again.
func (s *Store) absorb(bucket, key, absPath string, info os.FileInfo, entry *ManifestEntry) error {
	recorded, hasEntry := s.manifest.entry(bucket, key)
	if hasEntry && !recorded.stale(info.Size(), info.ModTime()) {
		return nil
	}
	derived := s.derive(absPath, info)
	if hasEntry {
		derived.VersionID = recorded.VersionID
		derived.ChecksumAlgorithm = recorded.ChecksumAlgorithm
		derived.ChecksumValue = recorded.ChecksumValue
	}
	derived.Form = s.formOf(bucket, key, absPath)
	if derived.Form == FormEscaped {
		derived.Digest = Digest(key)
	}
	*entry = derived
	return s.record(bucket, key, derived)
}

// record writes an entry to the manifest and the case-folded index, and
// persists. It takes the write lock, so it is only called from paths that do
// not already hold it.
func (s *Store) record(bucket, key string, entry ManifestEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	s.manifest.setEntry(bucket, key, entry)
	if entry.Form == FormNatural {
		s.folded[foldKey(bucket, key)] = key
	}
	return s.manifest.save()
}

// forget drops a key from the manifest and the index. It does not remove bytes;
// the caller does that.
func (s *Store) forget(bucket, key string) {
	s.manifest.removeEntry(bucket, key)
	delete(s.folded, foldKey(bucket, key))
}

// peek returns an object's current metadata for a conditional write, or
// ErrObjectNotFound with a nil meta when there is nothing there.
func (s *Store) peek(_ context.Context, bucket, key string) (*storage.ObjectMeta, error) {
	_, info, entry, err := s.resolve(bucket, key)
	if err != nil {
		return nil, err
	}
	meta := s.metaFromEntry(bucket, key, entry, info)
	return meta, nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
