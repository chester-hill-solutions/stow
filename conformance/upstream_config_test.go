package conformance_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/chester-hill-solutions/stow-s3/internal/storage"
)

func TestNormalizeLiveProvider(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		input string
		want  string
	}{
		{name: "aws", input: " AWS-S3 ", want: "aws-s3"},
		{name: "r2", input: "cloudflare-r2", want: "cloudflare-r2"},
		{name: "custom", input: "custom", want: "custom"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := normalizeLiveProvider(testCase.input)
			if err != nil {
				t.Fatalf("normalizeLiveProvider(%q): %v", testCase.input, err)
			}
			if got != testCase.want {
				t.Fatalf("normalizeLiveProvider(%q) = %q, want %q", testCase.input, got, testCase.want)
			}
		})
	}

	if _, err := normalizeLiveProvider("unknown"); err == nil {
		t.Fatal("expected unknown provider to be rejected")
	}
}

func TestNormalizeLivePrefix(t *testing.T) {
	got, err := normalizeLivePrefix(" ci/live-conformance/ ")
	if err != nil {
		t.Fatalf("normalizeLivePrefix: %v", err)
	}
	if got != "ci/live-conformance" {
		t.Fatalf("normalizeLivePrefix = %q, want %q", got, "ci/live-conformance")
	}

	for _, prefix := range []string{
		"",
		"/absolute",
		".",
		"..",
		"ci/../application",
		"ci//live",
		`ci\live`,
		"ci/\x00live",
	} {
		t.Run(strings.ReplaceAll(prefix, "/", "_"), func(t *testing.T) {
			if _, err := normalizeLivePrefix(prefix); err == nil {
				t.Fatalf("normalizeLivePrefix(%q) unexpectedly succeeded", prefix)
			}
		})
	}
}

func TestLiveObjectKeyIsProviderAndRunIsolated(t *testing.T) {
	got := liveObjectKey("ci/live", "aws-s3", "1234-2", 42)
	want := "ci/live/aws-s3/1234-2/42"
	if got != want {
		t.Fatalf("liveObjectKey = %q, want %q", got, want)
	}
}

func TestRemoveAndVerifyLiveObject(t *testing.T) {
	const bucket = "conformance-bucket"
	const key = "conformance/run/object"

	t.Run("deletes and verifies absence", func(t *testing.T) {
		client := &fakeLiveObjectCleaner{headErr: storage.ErrObjectNotFound}
		if err := removeAndVerifyLiveObject(context.Background(), client, bucket, key); err != nil {
			t.Fatalf("removeAndVerifyLiveObject: %v", err)
		}
		if client.deleteCalls != 1 || client.headCalls != 1 || client.listCalls != 1 {
			t.Fatalf("calls delete=%d head=%d list=%d", client.deleteCalls, client.headCalls, client.listCalls)
		}
		if client.listedOptions.Prefix != key {
			t.Fatalf("cleanup listing prefix = %q, want %q", client.listedOptions.Prefix, key)
		}
	})

	t.Run("rejects a delete failure", func(t *testing.T) {
		deleteErr := errors.New("delete failed")
		client := &fakeLiveObjectCleaner{deleteErr: deleteErr}
		err := removeAndVerifyLiveObject(context.Background(), client, bucket, key)
		if !errors.Is(err, deleteErr) {
			t.Fatalf("removeAndVerifyLiveObject error = %v, want %v", err, deleteErr)
		}
		if client.headCalls != 0 || client.listCalls != 0 {
			t.Fatalf("verification ran after delete failure: head=%d list=%d", client.headCalls, client.listCalls)
		}
	})

	t.Run("rejects a residual listing", func(t *testing.T) {
		client := &fakeLiveObjectCleaner{
			headErr: storage.ErrObjectNotFound,
			listed:  []storage.ObjectMeta{{Bucket: bucket, Key: key}},
		}
		err := removeAndVerifyLiveObject(context.Background(), client, bucket, key)
		if err == nil || !strings.Contains(err.Error(), "cleanup listing still contains") {
			t.Fatalf("removeAndVerifyLiveObject error = %v, want residual listing error", err)
		}
	})

	t.Run("times out when the head remains", func(t *testing.T) {
		client := &fakeLiveObjectCleaner{headExists: true}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
		defer cancel()
		err := removeAndVerifyLiveObject(ctx, client, bucket, key)
		if err == nil || !strings.Contains(err.Error(), "still exists") {
			t.Fatalf("removeAndVerifyLiveObject error = %v, want residual head error", err)
		}
	})
}

type fakeLiveObjectCleaner struct {
	deleteErr     error
	headErr       error
	headExists    bool
	listed        []storage.ObjectMeta
	listErr       error
	deleteCalls   int
	headCalls     int
	listCalls     int
	listedOptions storage.ListOptions
}

func (f *fakeLiveObjectCleaner) DeleteObject(context.Context, string, string) error {
	f.deleteCalls++
	return f.deleteErr
}

func (f *fakeLiveObjectCleaner) HeadObject(context.Context, string, string) (*storage.ObjectMeta, error) {
	f.headCalls++
	if f.headErr != nil {
		return nil, f.headErr
	}
	if f.headExists {
		return &storage.ObjectMeta{}, nil
	}
	return nil, storage.ErrObjectNotFound
}

func (f *fakeLiveObjectCleaner) ListObjectsV2(_ context.Context, _ string, options storage.ListOptions) (*storage.ListResult, error) {
	f.listCalls++
	f.listedOptions = options
	if f.listErr != nil {
		return nil, f.listErr
	}
	return &storage.ListResult{Objects: f.listed}, nil
}
