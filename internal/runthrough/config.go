package runthrough

import (
	"os"
	"strings"
)

// Mode describes whether stow runs local-only or with an upstream adapter.
type Mode string

const (
	ModeLocal      Mode = "local"
	ModeRunThrough Mode = "run-through"
)

// Policy controls how reads and writes interact with upstream storage.
type Policy string

const (
	PolicyProxy            Policy = "proxy"
	PolicyReadThroughCache Policy = "readThroughCache"
	PolicyMirrorWrites     Policy = "mirrorWrites"
)

// UpstreamConfig holds credentials and endpoint for upstream S3-compatible storage.
type UpstreamConfig struct {
	Endpoint  string
	AccessKey string
	SecretKey string
	Region    string
	// Bucket optionally restricts upstream access to a single bucket name.
	Bucket string
}

// Config is the full run-through adapter configuration.
type Config struct {
	Policy                 Policy
	AllowLiveWrites        bool
	Revalidate             bool
	CacheDir               string
	Upstream               UpstreamConfig
	EvictOnUpstreamMissing bool
}

// FromEnv resolves upstream credentials from environment variables with
// precedence STOW_* > S3_* > AWS_*. The bool is true when endpoint, access
// key, and secret key are all present (minimum signal for run-through).
func (UpstreamConfig) FromEnv() (UpstreamConfig, bool) {
	endpoint := envFirst(
		"STOW_ENDPOINT",
		"S3_ENDPOINT",
		"AWS_ENDPOINT_URL_S3",
		"AWS_ENDPOINT_URL",
		"AWS_ENDPOINT",
	)
	accessKey := envFirst(
		"STOW_ACCESS_KEY_ID",
		"S3_ACCESS_KEY_ID",
		"AWS_ACCESS_KEY_ID",
	)
	secretKey := envFirst(
		"STOW_SECRET_ACCESS_KEY",
		"S3_SECRET_ACCESS_KEY",
		"AWS_SECRET_ACCESS_KEY",
	)
	if endpoint == "" || accessKey == "" || secretKey == "" {
		return UpstreamConfig{}, false
	}
	return UpstreamConfig{
		Endpoint:  endpoint,
		AccessKey: accessKey,
		SecretKey: secretKey,
		Region: envFirst(
			"STOW_REGION",
			"S3_REGION",
			"AWS_REGION",
			"AWS_DEFAULT_REGION",
		),
		Bucket: envFirst("STOW_BUCKET", "S3_BUCKET"),
	}, true
}

// ConfigFromEnv builds a Config from environment variables.
func ConfigFromEnv() Config {
	upstream, hasUpstream := (UpstreamConfig{}).FromEnv()

	cfg := Config{
		Policy:                 PolicyReadThroughCache,
		Revalidate:             true,
		EvictOnUpstreamMissing: true,
		CacheDir:               os.Getenv("STOW_CACHE_DIR"),
	}
	if hasUpstream {
		cfg.Upstream = upstream
	}

	if p := os.Getenv("STOW_POLICY"); p != "" {
		cfg.Policy = Policy(p)
	}

	cfg.AllowLiveWrites = envTruthy("STOW_ALLOW_LIVE_WRITES")

	if cache := strings.ToLower(strings.TrimSpace(os.Getenv("STOW_CACHE"))); cache == "revalidate-never" {
		cfg.Revalidate = false
	}
	if v, ok := envBool("STOW_REVALIDATE"); ok {
		cfg.Revalidate = v
	}

	if cfg.Policy == PolicyMirrorWrites {
		if _, ok := os.LookupEnv("STOW_ALLOW_LIVE_WRITES"); !ok {
			cfg.AllowLiveWrites = true
		}
	}

	return cfg
}

// DetectMode returns local when STOW_MODE=local or upstream credentials are
// absent; otherwise run-through.
func DetectMode() Mode {
	if strings.EqualFold(strings.TrimSpace(os.Getenv("STOW_MODE")), "local") {
		return ModeLocal
	}
	if _, ok := (UpstreamConfig{}).FromEnv(); ok {
		return ModeRunThrough
	}
	return ModeLocal
}

func envFirst(keys ...string) string {
	for _, key := range keys {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	return ""
}

func envTruthy(key string) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	switch v {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func envBool(key string) (bool, bool) {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		return false, false
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true, true
	case "0", "false", "no", "off":
		return false, true
	default:
		return false, true
	}
}
