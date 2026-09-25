package conformance_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go/middleware"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/chester-hill-solutions/stow/internal/auth"
	"github.com/chester-hill-solutions/stow/internal/s3api"
	"github.com/chester-hill-solutions/stow/internal/storage"
	"github.com/chester-hill-solutions/stow/internal/storage/fs"
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
	Status   *responseStatusCapture
}

type responseStatusCapture struct {
	mu     sync.Mutex
	status int
}

func (c *responseStatusCapture) capture(stack *middleware.Stack) error {
	return stack.Deserialize.Add(middleware.DeserializeMiddlewareFunc("conformance-status", func(ctx context.Context, in middleware.DeserializeInput, next middleware.DeserializeHandler) (middleware.DeserializeOutput, middleware.Metadata, error) {
		out, metadata, err := next.HandleDeserialize(ctx, in)
		if err == nil {
			if response, ok := out.RawResponse.(*smithyhttp.Response); ok && response.Response != nil {
				c.mu.Lock()
				c.status = response.StatusCode
				c.mu.Unlock()
			}
		}
		return out, metadata, err
	}), middleware.After)
}

func (c *responseStatusCapture) StatusCode() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
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
		store, err = fs.NewFilesystemStore(t.TempDir())
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
	status := &responseStatusCapture{}
	client := newS3Client(t, endpoint, creds, status)

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
		Status:   status,
	}
}

func newS3Client(t *testing.T, endpoint string, creds auth.Credentials, status *responseStatusCapture) *s3.Client {
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
		o.APIOptions = append(o.APIOptions, status.capture)
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
