package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/chester-hill-solutions/stow-s3/internal/runthrough"
	"github.com/chester-hill-solutions/stow-s3/internal/runtime"
	"github.com/chester-hill-solutions/stow-s3/internal/s3api"
	"github.com/chester-hill-solutions/stow-s3/internal/storage"
)

// upstreamRecorder is an upstream that fails the test if it is ever reached.
type upstreamRecorder struct {
	server *httptest.Server
	hits   atomic.Int64
}

func newUpstreamRecorder(t *testing.T) *upstreamRecorder {
	t.Helper()
	rec := &upstreamRecorder{}
	rec.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		rec.hits.Add(1)
		http.Error(w, "upstream must not be reachable from local mode", http.StatusInternalServerError)
	}))
	t.Cleanup(rec.server.Close)
	return rec
}

func (r *upstreamRecorder) requireUntouched(t *testing.T, step string) {
	t.Helper()
	if got := r.hits.Load(); got != 0 {
		t.Fatalf("%s made %d upstream request(s)", step, got)
	}
}

// setHazardousEnv reproduces the environment that makes auto-detect choose
// run-through and, before this slice, made mirrorWrites grant live-write
// consent on its own: a .env copied from a staging machine.
func setHazardousEnv(t *testing.T, endpoint string) {
	t.Helper()
	t.Setenv("STOW_ENDPOINT", endpoint)
	t.Setenv("STOW_ACCESS_KEY_ID", "AKIASHAREDSTAGING")
	t.Setenv("STOW_SECRET_ACCESS_KEY", "shared-staging-secret")
	t.Setenv("STOW_POLICY", "mirrorWrites")
}

// assertHazardIsReal proves the environment really would have selected
// run-through and really would have granted consent, so a later assertion of
// "no upstream traffic" is not passing for a trivial reason.
func assertHazardIsReal(t *testing.T) {
	t.Helper()
	if mode := runthrough.DetectMode(); mode != runthrough.ModeRunThrough {
		t.Fatalf("test setup did not create the hazard: DetectMode = %q, want run-through", mode)
	}
	cfg := runthrough.ConfigFromEnv()
	if cfg.Policy != runthrough.PolicyMirrorWrites {
		t.Fatalf("policy = %q, want mirrorWrites", cfg.Policy)
	}
	if cfg.AllowLiveWrites {
		t.Fatal("mirrorWrites granted live-write consent on its own")
	}
	if got := runthrough.EffectiveWritePolicy(cfg); got != runthrough.WritePolicyMirrorWritesDisabled {
		t.Fatalf("write policy = %q, want %q", got, runthrough.WritePolicyMirrorWritesDisabled)
	}
}

// startLocalServer brings up the same wiring stow serve uses, in local mode.
func startLocalServer(t *testing.T, dataDir string, cfg runthrough.Config) *httptest.Server {
	t.Helper()
	store, adapter, err := buildStore(runthrough.DetectMode(), runtime.BackendMemory, dataDir, cfg)
	if err != nil {
		t.Fatalf("build store: %v", err)
	}
	if adapter != nil {
		t.Fatal("local mode constructed a run-through adapter")
	}
	srv, err := s3api.New(s3api.Config{Store: store, Auth: s3api.DevBypass, Host: "127.0.0.1", Port: 0})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	local := httptest.NewServer(srv.Handler())
	t.Cleanup(local.Close)
	return local
}

// request performs one S3 call and fails the test on an error status.
func request(t *testing.T, base, method, path, body string) *http.Response {
	t.Helper()
	var reader io.Reader = strings.NewReader(body)
	req, err := http.NewRequestWithContext(context.Background(), method, base+path, reader)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	if body != "" {
		req.ContentLength = int64(len(body))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	if resp.StatusCode >= 300 {
		resp.Body.Close()
		t.Fatalf("%s %s: status %d", method, path, resp.StatusCode)
	}
	return resp
}

// The hazard this guards: a developer's shell has STOW_POLICY=mirrorWrites and
// credentials for a shared bucket in a copied .env. Auto-detect sees those and
// selects run-through, and the mirrorWrites policy used to turn live writes on
// by itself. Two defenses are asserted, because either alone is not enough:
//
//  1. mirrorWrites alone no longer grants live-write consent (runthrough config);
//  2. a local-mode server builds no upstream client at all, so it holds no
//     object capable of reaching the provider even if consent were granted.
//
// Asserting only (1) rests the defense on a boolean. Asserting only (2) hides
// the fact that the consent flag was being misread.
func TestLocalModeMakesZeroUpstreamRequests(t *testing.T) {
	upstream := newUpstreamRecorder(t)
	setHazardousEnv(t, upstream.server.URL)
	assertHazardIsReal(t)

	// STOW_MODE=local is the documented override.
	t.Setenv("STOW_MODE", "local")
	if mode := runthrough.DetectMode(); mode != runthrough.ModeLocal {
		t.Fatalf("STOW_MODE=local did not force local mode: got %q", mode)
	}

	dataDir := t.TempDir()
	cfg := runthrough.ConfigFromEnv()
	cfg.CacheDir = filepath.Join(dataDir, "cache")
	local := startLocalServer(t, dataDir, cfg)

	// Every operation that would propagate if the store were an adapter.
	steps := []struct{ method, path, body string }{
		{http.MethodPut, "/agent-bucket", ""},
		{http.MethodPut, "/agent-bucket/input.json", `{"task":"summarize"}`},
		{http.MethodGet, "/agent-bucket/input.json", ""},
		{http.MethodHead, "/agent-bucket/input.json", ""},
		{http.MethodGet, "/agent-bucket?list-type=2", ""},
		{http.MethodDelete, "/agent-bucket/input.json", ""},
	}
	for _, step := range steps {
		resp := request(t, local.URL, step.method, step.path, step.body)
		resp.Body.Close()
		upstream.requireUntouched(t, step.method+" "+step.path)
	}

	// A server that silently did nothing locally would also make zero upstream
	// requests, so prove the data really round-tripped through local memory.
	body := "local-content"
	request(t, local.URL, http.MethodPut, "/agent-bucket/verify.json", body).Body.Close()
	resp := request(t, local.URL, http.MethodGet, "/agent-bucket/verify.json", "")
	defer resp.Body.Close()
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read verify body: %v", err)
	}
	if string(got) != body {
		t.Fatalf("object body = %q, want %q", got, body)
	}
	upstream.requireUntouched(t, "verify read")
}

// buildStore must not build an upstream client in local mode even when the
// configuration is the most aggressive the tool accepts.
func TestBuildStoreIgnoresUpstreamConfigInLocalMode(t *testing.T) {
	setHazardousEnv(t, "https://upstream.example")
	t.Setenv("STOW_ALLOW_LIVE_WRITES", "true")
	t.Setenv("STOW_MODE", "local")

	cfg := runthrough.ConfigFromEnv()
	dataDir := t.TempDir()
	cfg.CacheDir = filepath.Join(dataDir, "cache")

	store, adapter, err := buildStore(runthrough.DetectMode(), runtime.BackendMemory, dataDir, cfg)
	if err != nil {
		t.Fatalf("build store: %v", err)
	}
	if adapter != nil {
		t.Fatal("local mode built a run-through adapter despite live-write consent")
	}
	if _, ok := store.(*storage.MemoryStore); !ok {
		t.Fatalf("local mode store = %T, want *storage.MemoryStore", store)
	}
}
