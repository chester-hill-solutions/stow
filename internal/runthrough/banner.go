package runthrough

import (
	"fmt"
	"net/url"
	"strings"
)

// StartupBanner returns a loud multi-line summary of mode, upstream endpoint,
// cache policy, and write policy for logging at process start.
func StartupBanner(cfg Config, mode Mode) string {
	var b strings.Builder
	fmt.Fprintf(&b, "stow mode: %s\n", mode)

	if mode == ModeRunThrough {
		host := redactEndpoint(cfg.Upstream.Endpoint)
		fmt.Fprintf(&b, "  upstream: %s (credentials redacted)\n", host)
		if cfg.Upstream.Bucket != "" {
			fmt.Fprintf(&b, "  upstream bucket filter: %s\n", cfg.Upstream.Bucket)
		}
		fmt.Fprintf(&b, "  cache policy: %s\n", cfg.Policy)
		if cfg.Revalidate {
			fmt.Fprintf(&b, "  revalidation: enabled (ETag/Last-Modified)\n")
		} else {
			fmt.Fprintf(&b, "  revalidation: disabled\n")
		}
		if cfg.CacheDir != "" {
			fmt.Fprintf(&b, "  cache dir: %s\n", cfg.CacheDir)
		}
	} else {
		fmt.Fprintf(&b, "  cache policy: none\n")
	}

	writePolicy := "local-only"
	if cfg.AllowLiveWrites {
		writePolicy = "allowLiveWrites"
	}
	fmt.Fprintf(&b, "  write policy: %s\n", writePolicy)
	fmt.Fprintf(&b, "  override: STOW_MODE=local forces local-only\n")
	if mode == ModeRunThrough && !cfg.AllowLiveWrites {
		fmt.Fprintf(&b, "  hint: set STOW_ALLOW_LIVE_WRITES=true to propagate writes upstream\n")
	}
	return b.String()
}

func redactEndpoint(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "(not set)"
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	if u.Scheme != "" {
		return u.Scheme + "://" + u.Host
	}
	return u.Host
}
