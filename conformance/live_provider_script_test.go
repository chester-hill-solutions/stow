package conformance_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The live provider matrix is a release gate, so its decision table is covered
// here rather than only exercised by hand. Running the real script keeps the
// test honest about shell behaviour such as exit status and GITHUB_OUTPUT.
type liveProviderResolution struct {
	active   string
	provider string
	reason   string
}

func TestLiveProviderResolve(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("live-provider.sh requires a POSIX shell")
	}
	awsConfig := map[string]string{
		"STOW_ENDPOINT":           "https://s3.us-east-1.amazonaws.com",
		"STOW_ACCESS_KEY_ID":      "live-access-key",
		"STOW_SECRET_ACCESS_KEY":  "live-secret-key",
		"STOW_LIVE_BUCKET":        "stow-live",
		"STOW_LIVE_BUCKET_PREFIX": "ci/live",
	}

	for _, testCase := range []struct {
		name    string
		env     map[string]string
		want    liveProviderResolution
		wantErr bool
	}{
		{
			name: "aws profile against an aws endpoint runs",
			env:  withEnv(awsConfig, "STOW_LIVE_PROFILE", "aws-s3"),
			want: liveProviderResolution{active: "true", provider: "aws-s3", reason: "configured"},
		},
		{
			name: "r2 profile against an r2 endpoint runs",
			env: withEnv(withEnv(awsConfig, "STOW_ENDPOINT", "https://account.r2.cloudflarestorage.com"),
				"STOW_LIVE_PROFILE", "cloudflare-r2-custom"),
			want: liveProviderResolution{active: "true", provider: "cloudflare-r2", reason: "configured"},
		},
		{
			name: "custom endpoint with a port runs as custom",
			env: withEnv(withEnv(awsConfig, "STOW_ENDPOINT", "https://minio.internal:9000"),
				"STOW_LIVE_PROFILE", "cloudflare-r2-custom"),
			want: liveProviderResolution{active: "true", provider: "custom", reason: "configured"},
		},
		{
			name: "aws profile refuses a custom endpoint",
			env:  withEnv(withEnv(awsConfig, "STOW_ENDPOINT", "https://minio.internal:9000"), "STOW_LIVE_PROFILE", "aws-s3"),
			want: liveProviderResolution{active: "false", provider: "none", reason: "endpoint-does-not-match-profile"},
		},
		{
			name: "r2 profile refuses an aws endpoint",
			env:  withEnv(awsConfig, "STOW_LIVE_PROFILE", "cloudflare-r2-custom"),
			want: liveProviderResolution{active: "false", provider: "none", reason: "endpoint-does-not-match-profile"},
		},
		{
			name: "missing configuration skips by default",
			env:  map[string]string{"STOW_LIVE_PROFILE": "aws-s3"},
			want: liveProviderResolution{active: "false", provider: "none", reason: "missing-configuration"},
		},
		{
			name:    "missing configuration fails when the gate requires it",
			env:     withEnv(map[string]string{"STOW_LIVE_PROFILE": "aws-s3"}, "STOW_CONFORMANCE_REQUIRE_CONFIGURED", "true"),
			wantErr: true,
		},
		{
			name: "explicitly requesting the other profile skips",
			env: withEnv(withEnv(awsConfig, "STOW_LIVE_PROFILE", "aws-s3"),
				"STOW_CONFORMANCE_REQUESTED_PROVIDER", "cloudflare-r2-custom"),
			want: liveProviderResolution{active: "false", provider: "none", reason: "profile-not-selected"},
		},
		{
			name: "explicit request against a mismatched endpoint fails",
			env: withEnv(withEnv(withEnv(awsConfig, "STOW_ENDPOINT", "https://minio.internal:9000"),
				"STOW_LIVE_PROFILE", "aws-s3"), "STOW_CONFORMANCE_REQUESTED_PROVIDER", "aws-s3"),
			wantErr: true,
		},
		{
			name: "dry run reports the profile without configuration",
			env:  withEnv(map[string]string{"STOW_LIVE_PROFILE": "aws-s3"}, "STOW_CONFORMANCE_DRY_RUN", "true"),
			want: liveProviderResolution{active: "true", provider: "aws-s3", reason: "dry-run"},
		},
		{
			name:    "plaintext endpoints are rejected",
			env:     withEnv(withEnv(awsConfig, "STOW_ENDPOINT", "http://s3.amazonaws.com"), "STOW_LIVE_PROFILE", "aws-s3"),
			wantErr: true,
		},
		{
			name:    "an unknown profile is rejected",
			env:     map[string]string{"STOW_LIVE_PROFILE": "azure-blob"},
			wantErr: true,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			got, stderr, err := resolveLiveProvider(t, testCase.env)
			if testCase.wantErr {
				if err == nil {
					t.Fatalf("resolve succeeded with %+v, want failure", got)
				}
			} else {
				if err != nil {
					t.Fatalf("resolve failed: %v (stderr: %s)", err, stderr)
				}
				if got != testCase.want {
					t.Fatalf("resolve = %+v, want %+v", got, testCase.want)
				}
			}
			for _, secret := range []string{"live-access-key", "live-secret-key"} {
				if strings.Contains(stderr, secret) {
					t.Fatalf("resolve leaked %q to stderr: %s", secret, stderr)
				}
			}
		})
	}
}

func withEnv(base map[string]string, name, value string) map[string]string {
	merged := make(map[string]string, len(base)+1)
	for key, existing := range base {
		merged[key] = existing
	}
	merged[name] = value
	return merged
}

func resolveLiveProvider(t *testing.T, env map[string]string) (liveProviderResolution, string, error) {
	t.Helper()
	output := filepath.Join(t.TempDir(), "github-output")
	// A fresh environment keeps ambient STOW_* or AWS_* values in the developer
	// shell from deciding a release gate.
	cmd := exec.Command("./live-provider.sh", "resolve")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "GITHUB_OUTPUT=" + output}
	for name, value := range env {
		cmd.Env = append(cmd.Env, name+"="+value)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	err := cmd.Run()

	var resolution liveProviderResolution
	if data, readErr := os.ReadFile(output); readErr == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			key, value, found := strings.Cut(line, "=")
			if !found {
				continue
			}
			switch key {
			case "active":
				resolution.active = value
			case "provider":
				resolution.provider = value
			case "reason":
				resolution.reason = value
			}
		}
	}
	return resolution, stderr.String(), err
}
