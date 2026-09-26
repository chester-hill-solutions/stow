package main

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/chester-hill-solutions/stow-s3/internal/runthrough"
	"github.com/chester-hill-solutions/stow-s3/internal/runtime"
	"github.com/chester-hill-solutions/stow-s3/internal/storage"
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

func TestValidateLiveWriteBackendRejectsMemoryPropagation(t *testing.T) {
	config := runthrough.Config{Policy: runthrough.PolicyMirrorWrites, AllowLiveWrites: true}
	if err := validateLiveWriteBackend(runthrough.ModeRunThrough, runtime.BackendMemory, config); err == nil {
		t.Fatal("expected memory live-write configuration to be rejected")
	}
	if err := validateLiveWriteBackend(runthrough.ModeRunThrough, runtime.BackendFilesystem, config); err != nil {
		t.Fatalf("filesystem live-write configuration: %v", err)
	}
	if err := validateLiveWriteBackend(runthrough.ModeLocal, runtime.BackendMemory, config); err != nil {
		t.Fatalf("local memory configuration: %v", err)
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

func TestParseBackend(t *testing.T) {
	for _, tc := range []struct {
		flag string
		want runtime.Backend
	}{
		{"memory", runtime.BackendMemory},
		{"filesystem", runtime.BackendFilesystem},
		{" FILESYSTEM ", runtime.BackendFilesystem},
		{"Memory", runtime.BackendMemory},
	} {
		got, err := parseBackend(tc.flag)
		if err != nil {
			t.Fatalf("parseBackend(%q): %v", tc.flag, err)
		}
		if got != tc.want {
			t.Errorf("parseBackend(%q) = %q, want %q", tc.flag, got, tc.want)
		}
	}
	for _, flag := range []string{"", "bogus", "workspace"} {
		if _, err := parseBackend(flag); err == nil {
			t.Errorf("parseBackend(%q) accepted a backend the CLI cannot construct", flag)
		}
	}
}

// The readiness payload used to publish a persistence claim of its own and a
// hardcoded multipart=true beside it. Both happened to agree with the runtime
// only because the CLI could select two backends, so nothing caught it.
//
// This is the check that would have caught it: the payload must report the
// runtime's capabilities, unchanged, for every backend this command can select.
func TestReadinessCapabilitiesAreTheRuntimes(t *testing.T) {
	for _, backend := range []runtime.Backend{runtime.BackendMemory, runtime.BackendFilesystem} {
		store := openLocalStore(backend, t.TempDir())
		_, capabilities, err := bindNativeRuntimeStore(store, backend, nil, nativeStorageLimits{}, nil)
		if err != nil {
			t.Fatalf("bind %q: %v", backend, err)
		}
		published := readyMessage(readyDetails{
			backend:      backend,
			capabilities: capabilities,
			mode:         string(runthrough.ModeLocal),
		}).Capabilities

		if published.Persistent != capabilities.Persistent {
			t.Errorf("%s: payload claims persistent=%v, runtime reports %v",
				backend, published.Persistent, capabilities.Persistent)
		}
		if published.Multipart != capabilities.Multipart {
			t.Errorf("%s: payload claims multipart=%v, runtime reports %v",
				backend, published.Multipart, capabilities.Multipart)
		}
	}
}

// The readiness payload publishes a persistence claim about a store this
// command opens. Assert the claim is true of the store actually opened, rather
// than only that two expressions of it agree.
func TestTheStoreOpenedMatchesThePersistencePublished(t *testing.T) {
	for _, tc := range []struct {
		backend     runtime.Backend
		wantPersist bool
	}{
		{runtime.BackendMemory, false},
		{runtime.BackendFilesystem, true},
	} {
		dir := t.TempDir()
		ctx := context.Background()

		store := openLocalStore(tc.backend, dir)
		if err := store.CreateBucket(ctx, "persisted"); err != nil {
			t.Fatalf("%s: create bucket: %v", tc.backend, err)
		}
		body := bytes.NewReader([]byte("payload"))
		if _, err := store.PutObject(ctx, "persisted", "k", body, storage.PutOptions{}); err != nil {
			t.Fatalf("%s: put: %v", tc.backend, err)
		}

		bound, capabilities, err := bindNativeRuntimeStore(store, tc.backend, nil, nativeStorageLimits{}, nil)
		if err != nil {
			t.Fatalf("%s: bind: %v", tc.backend, err)
		}
		if capabilities.Persistent != tc.wantPersist {
			t.Errorf("%s: runtime reports persistent=%v, want %v", tc.backend, capabilities.Persistent, tc.wantPersist)
		}
		if err := bound.Close(); err != nil {
			t.Fatalf("%s: close: %v", tc.backend, err)
		}

		// And whether the bytes are actually still there.
		reopened := openLocalStore(tc.backend, dir)
		_, _, err = reopened.GetObject(ctx, "persisted", "k")
		survived := err == nil
		_ = reopened.Close()
		if survived != tc.wantPersist {
			t.Errorf("%s: object survived a reopen = %v, want %v (err was %v)",
				tc.backend, survived, tc.wantPersist, err)
		}
	}
}
