package conformance_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/chester-hill-solutions/stow/internal/auth"
	"github.com/chester-hill-solutions/stow/internal/s3api"
	"github.com/chester-hill-solutions/stow/internal/storage"
)

const testRegion = auth.DefaultRegion

// testEnv hosts a local stow s3api.Server with SigV4 auth and an AWS SDK v2 client.
type testEnv struct {
	t        *testing.T
	Server   *s3api.Server
	Creds    auth.Credentials
	Client   *s3.Client
	Presign  *s3.PresignClient
	Endpoint string
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()

	creds := auth.Credentials{
		AccessKeyID:     "AKIACONFORMANCETEST01",
		SecretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
	}

	var store storage.Store
	switch os.Getenv("STOW_CONFORMANCE_BACKEND") {
	case "", "memory":
		store = storage.NewMemoryStore()
	case "filesystem":
		var err error
		store, err = storage.NewFilesystemStore(t.TempDir())
		if err != nil {
			t.Fatalf("filesystem store: %v", err)
		}
	default:
		t.Fatalf("unknown STOW_CONFORMANCE_BACKEND %q", os.Getenv("STOW_CONFORMANCE_BACKEND"))
	}
	verifier := auth.NewVerifier(testRegion)
	srv, err := s3api.New(s3api.Config{
		Store:  store,
		Auth:   s3api.SigV4Auth(verifier, creds),
		Host:   "127.0.0.1",
		Port:   0,
		Region: testRegion,
	})
	if err != nil {
		t.Fatalf("s3api.New: %v", err)
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ListenAndServe()
	}()

	deadline := time.Now().Add(2 * time.Second)
	for srv.Addr() == "" && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if srv.Addr() == "" {
		t.Fatal("server failed to bind")
	}

	endpoint := "http://" + srv.Addr()
	client := newS3Client(t, endpoint, creds)

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		select {
		case err := <-errCh:
			if err != nil && err != http.ErrServerClosed {
				t.Logf("server exit: %v", err)
			}
		case <-time.After(2 * time.Second):
		}
		if err := store.Close(); err != nil {
			t.Logf("store close: %v", err)
		}
	})

	return &testEnv{
		t:        t,
		Server:   srv,
		Creds:    creds,
		Client:   client,
		Presign:  s3.NewPresignClient(client),
		Endpoint: endpoint,
	}
}

func newS3Client(t *testing.T, endpoint string, creds auth.Credentials) *s3.Client {
	t.Helper()

	cfg := aws.Config{
		Region: testRegion,
		Credentials: credentials.NewStaticCredentialsProvider(
			creds.AccessKeyID,
			creds.SecretAccessKey,
			"",
		),
	}

	return s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
	})
}

func uniqueBucket(t *testing.T, parts ...string) string {
	t.Helper()
	name := t.Name()
	for _, p := range parts {
		name += "/" + p
	}
	sum := sha256.Sum256([]byte(name))
	return fmt.Sprintf("conf-%x", sum[:8])
}

func createBucket(ctx context.Context, t *testing.T, client *s3.Client, bucket string) {
	t.Helper()
	_, err := client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String(bucket),
	})
	if err != nil {
		t.Fatalf("CreateBucket(%q): %v", bucket, err)
	}
}
