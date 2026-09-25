package runthrough_test

import (
	"os"
	"strings"
	"testing"

	"github.com/chester-hill-solutions/stow/internal/runthrough"
)

func TestUpstreamConfigFromEnv_Precedence(t *testing.T) {
	t.Setenv("STOW_ENDPOINT", "https://stow.example")
	t.Setenv("S3_ENDPOINT", "https://s3.example")
	t.Setenv("AWS_ENDPOINT_URL", "https://aws.example")
	t.Setenv("STOW_ACCESS_KEY_ID", "stow-key")
	t.Setenv("S3_ACCESS_KEY_ID", "s3-key")
	t.Setenv("AWS_ACCESS_KEY_ID", "aws-key")
	t.Setenv("STOW_SECRET_ACCESS_KEY", "stow-secret")
	t.Setenv("STOW_SESSION_TOKEN", "stow-session")
	t.Setenv("S3_SECRET_ACCESS_KEY", "s3-secret")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "aws-secret")
	t.Setenv("STOW_REGION", "stow-region")
	t.Setenv("AWS_DEFAULT_REGION", "aws-region")
	t.Setenv("STOW_BUCKET", "stow-bucket")
	t.Setenv("S3_BUCKET", "s3-bucket")

	cfg, ok := (runthrough.UpstreamConfig{}).FromEnv()
	if !ok {
		t.Fatal("expected upstream config")
	}
	if cfg.Endpoint != "https://stow.example" {
		t.Fatalf("endpoint = %q, want stow precedence", cfg.Endpoint)
	}
	if cfg.AccessKey != "stow-key" {
		t.Fatalf("access key = %q, want stow precedence", cfg.AccessKey)
	}
	if cfg.SecretKey != "stow-secret" {
		t.Fatalf("secret key = %q, want stow precedence", cfg.SecretKey)
	}
	if cfg.SessionToken != "stow-session" {
		t.Fatalf("session token = %q, want stow precedence", cfg.SessionToken)
	}
	if cfg.Region != "stow-region" {
		t.Fatalf("region = %q, want stow precedence", cfg.Region)
	}
	if cfg.Bucket != "stow-bucket" {
		t.Fatalf("bucket = %q, want stow precedence", cfg.Bucket)
	}
}

func TestUpstreamConfigFromEnv_FallsBackToS3AndAWS(t *testing.T) {
	os.Unsetenv("STOW_ENDPOINT")
	os.Unsetenv("STOW_ACCESS_KEY_ID")
	os.Unsetenv("STOW_SECRET_ACCESS_KEY")
	os.Unsetenv("STOW_REGION")
	os.Unsetenv("STOW_BUCKET")

	t.Setenv("S3_ENDPOINT", "https://s3.example")
	t.Setenv("S3_ACCESS_KEY_ID", "s3-key")
	t.Setenv("S3_SECRET_ACCESS_KEY", "s3-secret")
	t.Setenv("S3_REGION", "s3-region")
	t.Setenv("S3_BUCKET", "s3-bucket")

	cfg, ok := (runthrough.UpstreamConfig{}).FromEnv()
	if !ok {
		t.Fatal("expected upstream config from S3 vars")
	}
	if cfg.Endpoint != "https://s3.example" {
		t.Fatalf("endpoint = %q", cfg.Endpoint)
	}
	if cfg.AccessKey != "s3-key" || cfg.SecretKey != "s3-secret" {
		t.Fatalf("unexpected credentials: %+v", cfg)
	}
	if cfg.Region != "s3-region" || cfg.Bucket != "s3-bucket" {
		t.Fatalf("unexpected region/bucket: %+v", cfg)
	}

	os.Unsetenv("S3_ENDPOINT")
	os.Unsetenv("S3_ACCESS_KEY_ID")
	os.Unsetenv("S3_SECRET_ACCESS_KEY")
	os.Unsetenv("S3_REGION")
	os.Unsetenv("S3_BUCKET")

	t.Setenv("AWS_ENDPOINT_URL", "https://aws.example")
	t.Setenv("AWS_ACCESS_KEY_ID", "aws-key")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "aws-secret")
	t.Setenv("AWS_DEFAULT_REGION", "aws-region")

	cfg, ok = (runthrough.UpstreamConfig{}).FromEnv()
	if !ok {
		t.Fatal("expected upstream config from AWS vars")
	}
	if cfg.Endpoint != "https://aws.example" {
		t.Fatalf("endpoint = %q", cfg.Endpoint)
	}
	if cfg.Region != "aws-region" {
		t.Fatalf("region = %q", cfg.Region)
	}
}

func TestUpstreamConfigFromEnv_Incomplete(t *testing.T) {
	os.Clearenv()
	t.Setenv("STOW_ENDPOINT", "https://only-endpoint.example")
	_, ok := (runthrough.UpstreamConfig{}).FromEnv()
	if ok {
		t.Fatal("expected incomplete config")
	}
}

func TestDetectMode(t *testing.T) {
	os.Clearenv()
	if got := runthrough.DetectMode(); got != runthrough.ModeLocal {
		t.Fatalf("DetectMode() = %q, want local", got)
	}

	t.Setenv("STOW_ENDPOINT", "https://upstream.example")
	t.Setenv("STOW_ACCESS_KEY_ID", "key")
	t.Setenv("STOW_SECRET_ACCESS_KEY", "secret")
	if got := runthrough.DetectMode(); got != runthrough.ModeRunThrough {
		t.Fatalf("DetectMode() = %q, want run-through", got)
	}

	t.Setenv("STOW_MODE", "local")
	if got := runthrough.DetectMode(); got != runthrough.ModeLocal {
		t.Fatalf("DetectMode() with STOW_MODE=local = %q, want local", got)
	}
}

func TestConfigFromEnv_Revalidate(t *testing.T) {
	os.Clearenv()
	t.Setenv("STOW_ENDPOINT", "https://upstream.example")
	t.Setenv("STOW_ACCESS_KEY_ID", "key")
	t.Setenv("STOW_SECRET_ACCESS_KEY", "secret")

	cfg := runthrough.ConfigFromEnv()
	if !cfg.Revalidate {
		t.Fatal("expected revalidation enabled by default")
	}

	t.Setenv("STOW_CACHE", "revalidate-never")
	cfg = runthrough.ConfigFromEnv()
	if cfg.Revalidate {
		t.Fatal("expected revalidation disabled for revalidate-never")
	}

	t.Setenv("STOW_CACHE", "")
	t.Setenv("STOW_REVALIDATE", "false")
	cfg = runthrough.ConfigFromEnv()
	if cfg.Revalidate {
		t.Fatal("expected revalidation disabled via STOW_REVALIDATE=false")
	}
}

func TestConfigFromEnv_CacheLimits(t *testing.T) {
	os.Clearenv()
	t.Setenv("STOW_CACHE_MAX_BYTES", "4096")
	t.Setenv("STOW_CACHE_MAX_OBJECTS", "8")
	t.Setenv("STOW_CACHE_TTL", "30s")
	cfg := runthrough.ConfigFromEnv()
	if cfg.Cache.MaxBytes != 4096 || cfg.Cache.MaxObjects != 8 || cfg.Cache.TTL.String() != "30s" {
		t.Fatalf("cache limits = %+v", cfg.Cache)
	}
}

func TestConfigFromEnv_ZeroTTLDisablesExpiry(t *testing.T) {
	os.Clearenv()
	t.Setenv("STOW_CACHE_TTL", "0")
	cfg, err := runthrough.ConfigFromEnvChecked()
	if err != nil {
		t.Fatalf("checked config: %v", err)
	}
	if cfg.Cache.TTL != 0 {
		t.Fatalf("cache TTL = %s, want disabled", cfg.Cache.TTL)
	}
}

func TestConfigFromEnvCheckedRejectsInvalidCacheLimit(t *testing.T) {
	os.Clearenv()
	t.Setenv("STOW_CACHE_MAX_BYTES", "-1")
	if _, err := runthrough.ConfigFromEnvChecked(); err == nil {
		t.Fatal("expected invalid cache limit to be rejected")
	}
}

func TestConfigFromEnvCheckedRejectsInvalidRevalidate(t *testing.T) {
	os.Clearenv()
	t.Setenv("STOW_REVALIDATE", "garbage")
	if _, err := runthrough.ConfigFromEnvChecked(); err == nil {
		t.Fatal("expected invalid STOW_REVALIDATE to be rejected")
	}
	if !runthrough.ConfigFromEnv().Revalidate {
		t.Fatal("invalid direct config should retain the safe default")
	}
}

func TestConfigFromEnvPreservesInvalidPolicy(t *testing.T) {
	os.Clearenv()
	t.Setenv("STOW_POLICY", "proxy")
	cfg := runthrough.ConfigFromEnv()
	if cfg.Policy != runthrough.Policy("proxy") {
		t.Fatalf("policy = %q, want invalid value preserved", cfg.Policy)
	}
}

func TestConfigFromEnvCheckedRejectsUnknownPolicy(t *testing.T) {
	os.Clearenv()
	t.Setenv("STOW_POLICY", "proxy")
	if _, err := runthrough.ConfigFromEnvChecked(); err == nil {
		t.Fatal("expected proxy policy to be rejected")
	}
	t.Setenv("STOW_POLICY", "not-a-policy")
	if _, err := runthrough.ConfigFromEnvChecked(); err == nil {
		t.Fatal("expected unknown policy to be rejected")
	}
}

func TestConfigFromEnv_AllowLiveWrites(t *testing.T) {
	os.Clearenv()
	t.Setenv("STOW_ENDPOINT", "https://upstream.example")
	t.Setenv("STOW_ACCESS_KEY_ID", "key")
	t.Setenv("STOW_SECRET_ACCESS_KEY", "secret")

	cfg := runthrough.ConfigFromEnv()
	if cfg.AllowLiveWrites {
		t.Fatal("expected allowLiveWrites false by default")
	}

	t.Setenv("STOW_ALLOW_LIVE_WRITES", "true")
	cfg = runthrough.ConfigFromEnv()
	if !cfg.AllowLiveWrites {
		t.Fatal("expected allowLiveWrites true")
	}
}

func TestStartupBanner(t *testing.T) {
	cfg := runthrough.Config{
		Policy:          runthrough.PolicyReadThroughCache,
		AllowLiveWrites: false,
		Revalidate:      true,
		Upstream: runthrough.UpstreamConfig{
			Endpoint: "https://r2.example.com/bucket",
		},
	}
	banner := runthrough.StartupBanner(cfg, runthrough.ModeRunThrough)
	if banner == "" {
		t.Fatal("expected non-empty banner")
	}
	for _, want := range []string{"run-through", "readThroughCache", "local-only", "STOW_MODE=local"} {
		if !strings.Contains(banner, want) {
			t.Fatalf("banner missing %q:\n%s", want, banner)
		}
	}
}
