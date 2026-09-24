package s3api

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEnforceContentLengthRejectsMismatch(t *testing.T) {
	req := httptest.NewRequest("PUT", "/bucket/key", strings.NewReader("body"))
	req.ContentLength = 99
	if err := enforceContentLength(req); err == nil {
		t.Fatal("expected Content-Length mismatch")
	}
}
