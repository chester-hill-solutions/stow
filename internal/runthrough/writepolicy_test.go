package runthrough_test

import (
	"os"
	"strings"
	"testing"

	"github.com/chester-hill-solutions/stow-s3/internal/runthrough"
)

// A policy is a routing choice. Only the live-write flag is consent to mutate a
// real provider, so the two must stay independent. These cases are the table
// that the startup banner, the status payload, and startup validation all read
// through EffectiveWritePolicy, so a change to any of them fails here.
func TestEffectiveWritePolicy(t *testing.T) {
	cases := []struct {
		name     string
		policy   runthrough.Policy
		allow    bool
		want     string
		propagat bool
	}{
		{name: "read-through without consent stays local", policy: runthrough.PolicyReadThroughCache, want: runthrough.WritePolicyLocalOnly},
		{name: "read-through with consent propagates", policy: runthrough.PolicyReadThroughCache, allow: true, want: runthrough.WritePolicyAllowLiveWrites, propagat: true},
		{name: "mirrorWrites without consent stays local", policy: runthrough.PolicyMirrorWrites, want: runthrough.WritePolicyMirrorWritesDisabled},
		{name: "mirrorWrites with consent propagates", policy: runthrough.PolicyMirrorWrites, allow: true, want: runthrough.WritePolicyMirrorWrites, propagat: true},
		{name: "no policy with consent propagates", allow: true, want: runthrough.WritePolicyAllowLiveWrites, propagat: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := runthrough.Config{Policy: tc.policy, AllowLiveWrites: tc.allow}
			if got := runthrough.EffectiveWritePolicy(cfg); got != tc.want {
				t.Errorf("EffectiveWritePolicy = %q, want %q", got, tc.want)
			}
			if got := runthrough.PropagatesUpstream(cfg); got != tc.propagat {
				t.Errorf("PropagatesUpstream = %v, want %v", got, tc.propagat)
			}
		})
	}
}

// The regression this policy exists for: STOW_POLICY=mirrorWrites arriving from
// an inherited .env, next to credentials for a shared bucket, used to turn
// AllowLiveWrites on by itself. That is a silent write to someone else's data.
func TestConfigFromEnvMirrorWritesAloneDoesNotAllowLiveWrites(t *testing.T) {
	os.Clearenv()
	t.Setenv("STOW_ENDPOINT", "https://upstream.example")
	t.Setenv("STOW_ACCESS_KEY_ID", "key")
	t.Setenv("STOW_SECRET_ACCESS_KEY", "secret")
	t.Setenv("STOW_POLICY", "mirrorWrites")

	cfg := runthrough.ConfigFromEnv()
	if cfg.Policy != runthrough.PolicyMirrorWrites {
		t.Fatalf("policy = %q, want mirrorWrites", cfg.Policy)
	}
	if cfg.AllowLiveWrites {
		t.Fatal("mirrorWrites policy enabled live writes without STOW_ALLOW_LIVE_WRITES")
	}
	if got := runthrough.EffectiveWritePolicy(cfg); got != runthrough.WritePolicyMirrorWritesDisabled {
		t.Fatalf("write policy = %q, want %q", got, runthrough.WritePolicyMirrorWritesDisabled)
	}

	// The explicit opt-in is the only thing that turns it on, and it still
	// works, so this narrows the hazard without removing the feature.
	t.Setenv("STOW_ALLOW_LIVE_WRITES", "true")
	cfg = runthrough.ConfigFromEnv()
	if !cfg.AllowLiveWrites {
		t.Fatal("STOW_ALLOW_LIVE_WRITES=true did not enable live writes")
	}
	if got := runthrough.EffectiveWritePolicy(cfg); got != runthrough.WritePolicyMirrorWrites {
		t.Fatalf("write policy = %q, want %q", got, runthrough.WritePolicyMirrorWrites)
	}
}

// A false opt-out must not be able to look like consent either.
func TestConfigFromEnvExplicitFalseOptOutStaysLocal(t *testing.T) {
	os.Clearenv()
	t.Setenv("STOW_POLICY", "mirrorWrites")
	t.Setenv("STOW_ALLOW_LIVE_WRITES", "false")

	if cfg := runthrough.ConfigFromEnv(); cfg.AllowLiveWrites {
		t.Fatal("STOW_ALLOW_LIVE_WRITES=false enabled live writes")
	}
}

// The banner is the only thing an operator reads before finding out a write
// reached a real bucket, so it must not describe propagation that is off.
func TestStartupBannerReflectsEffectiveWritePolicy(t *testing.T) {
	upstream := runthrough.UpstreamConfig{Endpoint: "https://r2.example.com/bucket"}

	disabled := runthrough.StartupBanner(runthrough.Config{
		Policy:     runthrough.PolicyMirrorWrites,
		Revalidate: true,
		Upstream:   upstream,
	}, runthrough.ModeRunThrough)
	if !strings.Contains(disabled, runthrough.WritePolicyMirrorWritesDisabled) {
		t.Errorf("banner does not report the disabled policy:\n%s", disabled)
	}
	if strings.Contains(disabled, "mirrorWrites propagates supported mutations upstream") {
		t.Errorf("banner warns about propagation that is disabled:\n%s", disabled)
	}
	if !strings.Contains(disabled, "STOW_ALLOW_LIVE_WRITES=true") {
		t.Errorf("banner does not tell the operator how to opt in:\n%s", disabled)
	}

	enabled := runthrough.StartupBanner(runthrough.Config{
		Policy:          runthrough.PolicyMirrorWrites,
		AllowLiveWrites: true,
		Revalidate:      true,
		Upstream:        upstream,
	}, runthrough.ModeRunThrough)
	if !strings.Contains(enabled, "mirrorWrites propagates supported mutations upstream") {
		t.Errorf("banner does not warn about live propagation:\n%s", enabled)
	}
}
