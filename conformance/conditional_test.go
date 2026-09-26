package conformance_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

// A matching If-None-Match on a read is 304 Not Modified, not 412.
//
// This asserted 412 until the shared corpus case behind it was corrected. That
// was a divergence from S3 hiding inside the project's own contract: the corpus
// is normative, so the contract ratified the wrong answer and every runtime
// inherited it. Real S3 and MinIO both answer 304, and aws-sdk-go-v2 surfaces it
// as a NotModified API error, which is what this now pins.
func TestGetObjectWithAMatchingIfNoneMatchIsNotModified(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	bucket := uniqueBucket(t, "get-conditional")
	createBucket(ctx, t, env.Client, bucket)
	put, err := env.Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String("key"),
		Body:   strings.NewReader("body"),
	})
	if err != nil {
		t.Fatalf("PutObject: %v", err)
	}
	_, err = env.Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String("key"),
		IfNoneMatch: put.ETag,
	})
	if err == nil {
		t.Fatal("expected a conditional GET to report not-modified rather than return a body")
	}
	var responseErr *smithyhttp.ResponseError
	if !errors.As(err, &responseErr) || responseErr.HTTPStatusCode() != http.StatusNotModified {
		t.Fatalf("expected 304, got %v", err)
	}
}

// The mirror: a non-matching validator means the object changed, so the body is
// returned. A 304 here would tell a caching client to keep serving bytes it no
// longer has.
func TestGetObjectWithAStaleIfNoneMatchReturnsTheBody(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	bucket := uniqueBucket(t, "get-conditional-stale")
	createBucket(ctx, t, env.Client, bucket)
	if _, err := env.Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String("key"),
		Body:   strings.NewReader("body"),
	}); err != nil {
		t.Fatalf("PutObject: %v", err)
	}
	output, err := env.Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String("key"),
		IfNoneMatch: aws.String(`"a-stale-etag"`),
	})
	if err != nil {
		t.Fatalf("GetObject with a stale validator: %v", err)
	}
	defer output.Body.Close()
	body, err := io.ReadAll(output.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(body) != "body" {
		t.Errorf("body = %q, want %q", body, "body")
	}
}
