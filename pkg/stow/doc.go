// Package stow provides the direct, in-process object runtime.
//
// Open exposes the memory-only embedded profile. OpenWorkspace exposes the
// default agent surface: a bounded artifact workspace whose objects are real
// files in a directory the caller is already working in, so a local path and an
// s3:// key are the same bytes. See docs/workspace-contract.md for its contract
// and docs/adr/0007-workspace-is-the-default.md for why it is the default.
//
// The S3-compatible HTTP service remains available through the repository's
// process and TypeScript entry points.
package stow
