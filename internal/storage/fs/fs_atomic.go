package fs

import (
	"encoding/json"

	"github.com/chester-hill-solutions/stow-s3/internal/atomicfile"
)

// writeBytesAtomic and writeJSONAtomic delegate to internal/atomicfile so that
// there is one atomic-write implementation with one guarantee. The copy that
// lived here synced the parent directory and discarded both the open and the sync
// error, so a failed sync reported success; the workspace manifest did not sync it
// at all. The guarantee is now stated once, in one place, and it includes the
// errors.
func writeBytesAtomic(path string, data []byte) error {
	return atomicfile.Write(path, data, 0o644)
}

func writeJSONAtomic(path string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return writeBytesAtomic(path, data)
}
