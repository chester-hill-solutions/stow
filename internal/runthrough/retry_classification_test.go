package runthrough_test

import (
	"errors"
	"testing"
	"time"

	"github.com/chester-hill-solutions/stow/internal/runthrough"
)

func TestRetryClassification(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want runthrough.RetryClass
	}{
		{name: "bad request", err: runthrough.NewUpstreamError(400, errors.New("bad request")), want: runthrough.RetryClassDeterministic},
		{name: "throttled", err: runthrough.NewUpstreamError(429, errors.New("slow down")), want: runthrough.RetryClassTransient},
		{name: "unavailable", err: runthrough.NewUpstreamError(503, errors.New("unavailable")), want: runthrough.RetryClassTransient},
		{name: "version conflict", err: runthrough.ErrOutboxVersionConflict, want: runthrough.RetryClassDeterministic},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := runthrough.ClassifyRetry(tc.err); got != tc.want {
				t.Fatalf("class = %v, want %v", got, tc.want)
			}
		})
	}

	outbox := runthrough.NewMemoryOutbox()
	entry, err := outbox.Enqueue(runthrough.OutboxEntry{Operation: runthrough.OutboxPut, Bucket: "bucket", Key: "key"})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := outbox.MarkFailure(entry.ID, runthrough.NewUpstreamError(400, errors.New("bad request")), time.Now()); err != nil {
		t.Fatalf("mark failure: %v", err)
	}
	pending := outbox.Pending()
	if len(pending) != 1 || !pending[0].Terminal || !pending[0].NextAttempt.IsZero() {
		t.Fatalf("deterministic failure was not terminal: %+v", pending)
	}
}
