package fs

import "encoding/hex"

// objectRelPath returns a reversible, flat filesystem name for an object key.
// Encoding the complete key avoids path traversal and preserves empty, repeated,
// and dot path segments.
func objectRelPath(key string) string {
	return hex.EncodeToString([]byte(key))
}

func objectKeyFromFilename(name string) (string, bool) {
	decoded, err := hex.DecodeString(name)
	if err != nil {
		return "", false
	}
	return string(decoded), true
}
