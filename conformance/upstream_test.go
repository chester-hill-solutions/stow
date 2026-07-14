package conformance_test

import (
	"os"
	"testing"
)

// Upstream / run-through conformance tests require a live S3-compatible provider.
// Set STOW_CONFORMANCE_UPSTREAM=1 and configure STOW_* / AWS_* env vars.
func TestUpstreamRunThrough(t *testing.T) {
	if os.Getenv("STOW_CONFORMANCE_UPSTREAM") != "1" {
		t.Skip("skipping upstream conformance; set STOW_CONFORMANCE_UPSTREAM=1")
	}
	t.Skip("upstream run-through conformance not yet implemented")
}
