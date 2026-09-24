package storage

import "testing"

func TestWritePreconditionsRespectWeakValidators(t *testing.T) {
	existing := &ObjectMeta{ETag: `"abc"`}
	if err := checkWritePreconditions(PutOptions{IfNoneMatch: `W/"abc"`}, existing); err != ErrPreconditionFailed {
		t.Fatalf("weak If-None-Match error = %v", err)
	}
	if err := checkWritePreconditions(PutOptions{IfMatch: `W/"abc"`}, existing); err != ErrPreconditionFailed {
		t.Fatalf("weak If-Match error = %v", err)
	}
	if err := checkWritePreconditions(PutOptions{IfMatch: `"abc"`}, existing); err != nil {
		t.Fatalf("strong If-Match error = %v", err)
	}
}
