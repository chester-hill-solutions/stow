package main

import (
	"testing"
	"time"

	"github.com/chester-hill-solutions/stow/internal/runthrough"
)

func TestResolveLocalCredentials(t *testing.T) {
	t.Setenv("STOW_LOCAL_ACCESS_KEY_ID", "env-access")
	t.Setenv("STOW_LOCAL_SECRET_ACCESS_KEY", "env-secret")

	tests := []struct {
		name       string
		accessKey  string
		secretKey  string
		wantAccess string
		wantSecret string
	}{
		{
			name:       "flags win",
			accessKey:  "flag-access",
			secretKey:  "flag-secret",
			wantAccess: "flag-access",
			wantSecret: "flag-secret",
		},
		{
			name:       "environment fallback",
			wantAccess: "env-access",
			wantSecret: "env-secret",
		},
		{
			name:       "whitespace falls back",
			accessKey:  "  ",
			secretKey:  "\t",
			wantAccess: "env-access",
			wantSecret: "env-secret",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			accessKey, secretKey := resolveLocalCredentials(test.accessKey, test.secretKey)
			if accessKey != test.wantAccess || secretKey != test.wantSecret {
				t.Fatalf("credentials = %q/%q, want %q/%q", accessKey, secretKey, test.wantAccess, test.wantSecret)
			}
		})
	}
}

func TestApplyCacheLimitsPreservesEnvironmentDefaults(t *testing.T) {
	config := runthrough.Config{Cache: runthrough.CachePolicy{MaxBytes: 10, MaxObjects: 2, TTL: time.Minute}}
	if err := applyCacheLimits(&config, -1, -1, -1); err != nil {
		t.Fatalf("unset limits: %v", err)
	}
	if config.Cache.MaxBytes != 10 || config.Cache.MaxObjects != 2 || config.Cache.TTL != time.Minute {
		t.Fatalf("unset limits changed config: %+v", config.Cache)
	}
	if err := applyCacheLimits(&config, 20, 3, 2*time.Second); err != nil {
		t.Fatalf("explicit limits: %v", err)
	}
	if config.Cache.MaxBytes != 20 || config.Cache.MaxObjects != 3 || config.Cache.TTL != 2*time.Second {
		t.Fatalf("explicit limits = %+v", config.Cache)
	}
	if err := applyCacheLimits(&config, -2, 0, 0); err == nil {
		t.Fatal("expected negative cache limit error")
	}
}
