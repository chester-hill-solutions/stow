package runthrough

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
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
	PolicyReadThroughCache Policy = "readThroughCache"
	PolicyMirrorWrites     Policy = "mirrorWrites"
)

// UpstreamConfig holds credentials and endpoint for upstream S3-compatible storage.
type UpstreamConfig struct {
	Endpoint     string
	AccessKey    string
	SecretKey    string
	SessionToken string
	Region       string
	// Bucket optionally restricts upstream access to a single bucket name.
	Bucket string
}

// CachePolicy bounds the separate upstream-derived cache. Zero values disable
// the corresponding limit.
type CachePolicy struct {
	MaxBytes   int64
	MaxObjects int64
	TTL        time.Duration
}

// Config is the full run-through adapter configuration.
type Config struct {
	Policy                 Policy
	AllowLiveWrites        bool
	Revalidate             bool
	CacheDir               string
	Upstream               UpstreamConfig
	EvictOnUpstreamMissing bool
	Cache                  CachePolicy
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
	sessionToken := envFirst(
		"STOW_SESSION_TOKEN",
		"S3_SESSION_TOKEN",
		"AWS_SESSION_TOKEN",
	)
	if endpoint == "" || accessKey == "" || secretKey == "" {
		return UpstreamConfig{}, false
	}
	return UpstreamConfig{
		Endpoint:     endpoint,
		AccessKey:    accessKey,
		SecretKey:    secretKey,
		SessionToken: sessionToken,
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
		if policy, ok := ParsePolicy(p); ok {
			cfg.Policy = policy
		} else {
			// Preserve an invalid value so checked startup rejects it and
			// direct callers cannot silently fall back to a live-write policy.
			cfg.Policy = Policy(strings.TrimSpace(p))
		}
	}

	cfg.AllowLiveWrites = envTruthy("STOW_ALLOW_LIVE_WRITES")

	if cache := strings.ToLower(strings.TrimSpace(os.Getenv("STOW_CACHE"))); cache == "revalidate-never" {
		cfg.Revalidate = false
	}
	if value, present, err := parseEnvBool("STOW_REVALIDATE"); present && err == nil {
		cfg.Revalidate = value
	}

	if cfg.Policy == PolicyMirrorWrites {
		if _, ok := os.LookupEnv("STOW_ALLOW_LIVE_WRITES"); !ok {
			cfg.AllowLiveWrites = true
		}
	}

	_ = applyCacheEnv(&cfg)
	return cfg
}

// ConfigFromEnvChecked resolves configuration and reports invalid policy values.
// Callers that start a server should use this instead of silently accepting an
// unknown STOW_POLICY value.
func ConfigFromEnvChecked() (Config, error) {
	cfg := ConfigFromEnv()
	if err := applyCacheEnv(&cfg); err != nil {
		return cfg, err
	}
	if raw, present := os.LookupEnv("STOW_REVALIDATE"); present && strings.TrimSpace(raw) != "" {
		if _, _, err := parseEnvBool("STOW_REVALIDATE"); err != nil {
			return cfg, err
		}
	}
	if raw := strings.TrimSpace(os.Getenv("STOW_POLICY")); raw != "" {
		if _, ok := ParsePolicy(raw); !ok {
			return cfg, fmt.Errorf("invalid STOW_POLICY %q", raw)
		}
	}
	return cfg, nil
}

func applyCacheEnv(cfg *Config) error {
	for _, item := range []struct {
		name  string
		field *int64
	}{
		{name: "STOW_CACHE_MAX_BYTES", field: &cfg.Cache.MaxBytes},
		{name: "STOW_CACHE_MAX_OBJECTS", field: &cfg.Cache.MaxObjects},
	} {
		raw, ok := os.LookupEnv(item.name)
		if !ok || strings.TrimSpace(raw) == "" {
			continue
		}
		value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil || value < 0 {
			return fmt.Errorf("invalid %s %q: expected a non-negative integer", item.name, raw)
		}
		*item.field = value
	}
	if raw, ok := os.LookupEnv("STOW_CACHE_TTL"); ok && strings.TrimSpace(raw) != "" {
		value := strings.TrimSpace(raw)
		var ttl time.Duration
		if value == "0" {
			ttl = 0
		} else {
			parsed, err := time.ParseDuration(value)
			if err != nil {
				return fmt.Errorf("invalid STOW_CACHE_TTL %q: expected a non-negative duration", raw)
			}
			ttl = parsed
		}
		if ttl < 0 {
			return fmt.Errorf("invalid STOW_CACHE_TTL %q: expected a non-negative duration", raw)
		}
		cfg.Cache.TTL = ttl
	}
	return nil
}

// ParsePolicy maps a string to a known public Policy.
func ParsePolicy(raw string) (Policy, bool) {
	switch Policy(strings.TrimSpace(raw)) {
	case PolicyReadThroughCache, PolicyMirrorWrites:
		return Policy(strings.TrimSpace(raw)), true
	default:
		return "", false
	}
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

func parseEnvBool(key string) (bool, bool, error) {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return false, false, nil
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true, true, nil
	case "0", "false", "no", "off":
		return false, true, nil
	default:
		return false, true, fmt.Errorf("invalid %s %q: expected a boolean", key, raw)
	}
}
