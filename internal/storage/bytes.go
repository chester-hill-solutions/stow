package storage

import "io"

// ByteReader is a reader that already holds its whole content in memory and can
// hand it over without another copy.
//
// It exists so a caller that has already materialised a body does not pay for a
// second full-size buffer on the way to the store.
//
// Ownership: the bytes belong to the reader, and a consumer must treat them as
// read-only. A consumer that needs to keep them past the call must copy. Every
// store in this package does exactly that, through ETagForReader, which is what
// lets the runtime hand a caller's buffer to a store without copying it again.
// The runtime must therefore not hand a store a reader that exposes those bytes,
// or a store could adopt a buffer the caller still owns and may reuse. That rule
// is pinned by TestRuntimeNeverExposesCallerBytesToTheStore.
type ByteReader interface {
	io.Reader
	// Bytes returns the reader's content. The returned slice is owned by the
	// receiver and must not be modified by the caller.
	Bytes() []byte
}

// BytesOf returns a reader's content without copying it when the reader already
// holds it in memory, and by reading it once when it does not. The returned
// bytes are owned by whoever called BytesOf and must be treated as read-only.
func BytesOf(r io.Reader) ([]byte, error) {
	if r == nil {
		return nil, nil
	}
	if holder, ok := r.(ByteReader); ok {
		return holder.Bytes(), nil
	}
	return io.ReadAll(r)
}
