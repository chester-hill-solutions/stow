package fs

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
)

// OwnerMarkerName is the file that records a directory as a stow data
// directory. It is the only thing that distinguishes a directory stow created
// from one a developer happened to point at, so it is what makes it safe for a
// client to delete a data directory on the caller's behalf.
//
// The value is duplicated in packages/stow/src/ownership.ts, which reads it
// before deleting anything. scripts/check-version.mjs fails if the two drift.
const OwnerMarkerName = ".stow-owner"

// OwnerMarkerVersion is the current marker payload version. It is bumped if the
// payload gains meaning that a client would have to understand.
const OwnerMarkerVersion = 1

// OwnerMarker is the on-disk payload of OwnerMarkerName.
type OwnerMarker struct {
	Owner   string `json:"owner"`
	Version int    `json:"version"`
}

// IsOwnedDataDir reports whether dataDir carries a stow ownership marker.
// A directory that does not is never deleted on a caller's behalf, because
// there is no evidence stow created it.
func IsOwnedDataDir(dataDir string) bool {
	_, err := os.Stat(filepath.Join(dataDir, OwnerMarkerName))
	return err == nil
}

// writeOwnerMarker records that stow owns dataDir.
//
// A failure here is deliberately not fatal. The marker's absence only makes
// later cleanup refuse to delete the directory, which is the safe direction to
// fail in, and refusing to start a server over a read-only data directory would
// be a worse outcome than a data directory that cannot be reset.
func writeOwnerMarker(dataDir string) {
	if IsOwnedDataDir(dataDir) {
		return
	}
	payload, err := json.Marshal(OwnerMarker{Owner: "stow", Version: OwnerMarkerVersion})
	if err != nil {
		log.Printf("stow: encode ownership marker: %v", err)
		return
	}
	path := filepath.Join(dataDir, OwnerMarkerName)
	if err := os.WriteFile(path, append(payload, '\n'), 0o644); err != nil {
		log.Printf("stow: write ownership marker %s: %v (this data directory will not be resettable)", path, err)
	}
}

// OwnedDataDirError explains why a directory was not deleted, so a caller who
// expected a reset gets an actionable message rather than a silent no-op.
type OwnedDataDirError struct {
	Path   string
	Reason string
}

func (e *OwnedDataDirError) Error() string {
	return fmt.Sprintf("refusing to delete %s: %s", e.Path, e.Reason)
}
