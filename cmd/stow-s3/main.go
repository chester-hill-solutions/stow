package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/chester-hill-solutions/stow-s3/internal/auth"
	"github.com/chester-hill-solutions/stow-s3/internal/parentwatch"
	"github.com/chester-hill-solutions/stow-s3/internal/ready"
	"github.com/chester-hill-solutions/stow-s3/internal/runthrough"
	"github.com/chester-hill-solutions/stow-s3/internal/s3api"
	"github.com/chester-hill-solutions/stow-s3/internal/storage"
	"github.com/chester-hill-solutions/stow-s3/internal/storage/fs"
	"github.com/chester-hill-solutions/stow-s3/internal/version"
)

// readyDetails is what the readiness announcement needs to describe the server.
type readyDetails struct {
	endpoint string
	mode     string
	backend  string
	limits   nativeStorageLimits
	creds    auth.Credentials
	banner   string
	// region is the region the server actually verifies signatures against, so
	// a client that honors the reported region signs correctly.
	region string
}

// announceStartup prints the human-readable startup output and then publishes
// readiness. With a ready descriptor the versioned JSON object is written to it
// and the legacy STOW_READY line is deliberately not printed, so credentials
// stay off stdout for a client that asked for the versioned channel.
func announceStartup(readyFd int, details readyDetails) {
	fmt.Print(details.banner)
	fmt.Printf("stow listening on %s (mode=%s)\n", details.endpoint, details.mode)
	if readyFd >= 0 {
		writeReadyMessage(readyFd, details)
		return
	}
	fmt.Printf(
		"STOW_READY endpoint=%s access_key=%s secret_key=%s mode=%s\n",
		details.endpoint,
		details.creds.AccessKeyID,
		details.creds.SecretAccessKey,
		details.mode,
	)
}

func writeReadyMessage(fd int, details readyDetails) {
	message := ready.New(ready.Input{
		Endpoint:      details.endpoint,
		Region:        details.region,
		AccessKeyID:   details.creds.AccessKeyID,
		SecretKey:     details.creds.SecretAccessKey,
		Mode:          details.mode,
		Backend:       details.backend,
		BinaryVersion: version.Version,
		Capabilities: ready.Capabilities{
			Persistent:        details.backend == "filesystem",
			Multipart:         true,
			Upstream:          details.mode == string(runthrough.ModeRunThrough),
			ConditionalWrites: true,
			PresignedURLs:     true,
			MaxBytes:          ready.ReportedLimit(details.limits.bytes()),
			MaxObjects:        ready.ReportedLimit(details.limits.objects()),
			MaxRequestBytes:   s3api.DefaultMaxRequestBytes,
		},
	})
	if err := ready.WriteToFd(fd, message); err != nil {
		log.Fatalf("write ready message: %v", err)
	}
}

// openLocalStore creates the authoritative local store for the selected
// backend. Backend selection is a startup decision, so an unknown value or a
// failed open is fatal rather than an error a caller can recover from.
func openLocalStore(backend, dataDir string) storage.Store {
	switch backend {
	case "filesystem":
		store, err := fs.NewFilesystemStore(dataDir)
		if err != nil {
			log.Fatalf("open store: %v", err)
		}
		return store
	case "memory":
		return storage.NewMemoryStore()
	default:
		log.Fatalf("invalid --backend %q (want filesystem or memory)", backend)
		return nil
	}
}

// armParentWatch installs the opt-in parent-death watch.
//
// Only a session passes a parent pid, so a hand-run server keeps its existing
// behavior of surviving its shell. A pid that is not this process's parent is
// fatal rather than watched, because watching a stranger would leave the server
// serving with no safety net while appearing protected. A platform with no
// mechanism is a missing safety net, not a reason to refuse to serve, but it
// must never be silent.
func armParentWatch(pid int) {
	request := parentwatch.Requested{Pid: pid, Getppid: os.Getppid}
	switch err := parentwatch.Watch(request); {
	case err == nil:
		log.Printf("will exit when parent process %d dies", pid)
	case errors.Is(err, parentwatch.ErrNotParent):
		log.Fatalf("refusing to start: %v", err)
	default:
		log.Printf("WARNING: parent-death watch unavailable: %v", err)
	}
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "serve":
		serve(os.Args[2:])
	case "doctor":
		doctor(os.Args[2:])
	default:
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, "usage: stow-s3 <command>\n\ncommands:\n  serve    start the S3-compatible server\n  doctor   report whether this machine can run a stow-s3 session\n")
}

func resolveLocalCredentials(accessKey, secretKey string) (string, string) {
	if strings.TrimSpace(accessKey) == "" {
		accessKey = os.Getenv("STOW_LOCAL_ACCESS_KEY_ID")
	}
	if strings.TrimSpace(secretKey) == "" {
		secretKey = os.Getenv("STOW_LOCAL_SECRET_ACCESS_KEY")
	}
	return accessKey, secretKey
}

func applyCacheLimits(config *runthrough.Config, maxBytes, maxObjects int64, ttl time.Duration) error {
	if maxBytes < -1 || maxObjects < -1 || ttl < -1 {
		return fmt.Errorf("cache limits must not be negative")
	}
	if maxBytes >= 0 {
		config.Cache.MaxBytes = maxBytes
	}
	if maxObjects >= 0 {
		config.Cache.MaxObjects = maxObjects
	}
	if ttl >= 0 {
		config.Cache.TTL = ttl
	}
	return nil
}

func validateLiveWriteBackend(mode runthrough.Mode, backend string, config runthrough.Config) error {
	// Keyed on consent, not policy. A mirrorWrites policy without
	// STOW_ALLOW_LIVE_WRITES keeps every write local, so it is perfectly
	// serviceable from the memory backend and must not be refused here.
	if mode == runthrough.ModeRunThrough && backend == "memory" && runthrough.PropagatesUpstream(config) {
		return fmt.Errorf("run-through live writes require the filesystem backend")
	}
	return nil
}

func startOutboxRetryWorker(adapter *runthrough.Adapter) (context.CancelFunc, <-chan struct{}) {
	if adapter == nil {
		done := make(chan struct{})
		close(done)
		return func() {}, done
	}
	retryCtx, retryCancel := context.WithCancel(context.Background())
	retryDone := make(chan struct{})
	go func() {
		defer close(retryDone)
		_ = adapter.RetryPending(retryCtx)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-retryCtx.Done():
				return
			case <-ticker.C:
				_ = adapter.RetryPending(retryCtx)
			}
		}
	}()
	return retryCancel, retryDone
}

func serve(args []string) {
	flags := flag.NewFlagSet("serve", flag.ExitOnError)
	port := flags.Int("port", 9000, "HTTP listen port (0 = ephemeral)")
	dataDir := flags.String("data-dir", ".stow", "Data directory for object storage")
	backend := flags.String("backend", "filesystem", "Storage backend (filesystem or memory)")
	accessKey := flags.String("access-key", "", "Access key (generated if omitted)")
	secretKey := flags.String("secret-key", "", "Secret key (generated if omitted)")
	host := flags.String("host", "127.0.0.1", "Listen host")
	baseHost := flags.String("base-host", "", "Host suffix for virtual-hosted-style routing")
	allowPublicAdmin := flags.Bool("allow-public-admin", false, "Deprecated and ignored: remote admin routes now require --admin-token")
	adminToken := flags.String("admin-token", "", "Credential for admin and metrics routes; prefer STOW_ADMIN_TOKEN so it is not visible in the process list")
	var corsOrigins corsOriginList
	flags.Var(&corsOrigins, "cors-origin", "Browser origin permitted to read responses; repeatable. Default permits loopback origins only")
	modeFlag := flags.String("mode", "auto", "Operational mode: local, run-through, or auto (default)")
	regionFlag := flags.String("region", auth.DefaultRegion, "Region the server verifies signatures against and reports in the readiness message")
	allowLiveWrites := flags.Bool("allow-live-writes", false, "Propagate writes to upstream S3")
	cacheDir := flags.String("cache-dir", "", "Run-through cache directory (default: <data-dir>/cache)")
	cacheMaxBytes := flags.Int64("cache-max-bytes", -1, "Maximum separate cache bytes (0 disables the limit; -1 uses environment)")
	cacheMaxObjects := flags.Int64("cache-max-objects", -1, "Maximum separate cache objects (0 disables the limit; -1 uses environment)")
	cacheTTL := flags.Duration("cache-ttl", -1, "Separate cache entry lifetime (0 disables expiry; -1 uses environment)")
	maxBytes := flags.Int64("max-bytes", 0, "Maximum stored object bytes enforced on every native S3 request (0 disables the limit)")
	maxObjects := flags.Int64("max-objects", 0, "Maximum stored object count enforced on every native S3 request (0 disables the limit)")
	readyFd := flags.Int("ready-fd", -1, "Write one machine-readable readiness object to this file descriptor instead of the STOW_READY line on stdout")
	parentPid := flags.Int("parent-pid", 0, "Exit when this parent process dies (0 disables the watch, which is the default for a hand-run server)")
	flags.Parse(args)
	*accessKey, *secretKey = resolveLocalCredentials(*accessKey, *secretKey)
	rtCfg, cfgErr := runthrough.ConfigFromEnvChecked()
	if cfgErr != nil {
		log.Fatal(cfgErr)
	}
	if err := applyCacheLimits(&rtCfg, *cacheMaxBytes, *cacheMaxObjects, *cacheTTL); err != nil {
		log.Fatal(err)
	}
	mode := runthrough.DetectMode()
	switch strings.ToLower(strings.TrimSpace(*modeFlag)) {
	case "auto", "":
	case "local":
		mode = runthrough.ModeLocal
	case "run-through":
		mode = runthrough.ModeRunThrough
	default:
		log.Fatalf("invalid --mode %q (want local, run-through, or auto)", *modeFlag)
	}

	if *allowPublicAdmin {
		log.Printf("WARNING: --allow-public-admin no longer grants access and is ignored; use --admin-token or STOW_ADMIN_TOKEN to authorize remote admin routes")
	}
	if *adminToken == "" {
		*adminToken = strings.TrimSpace(os.Getenv("STOW_ADMIN_TOKEN"))
	}
	if *allowLiveWrites {
		rtCfg.AllowLiveWrites = true
	}
	if *cacheDir != "" {
		rtCfg.CacheDir = *cacheDir
	}
	if err := validateLiveWriteBackend(mode, *backend, rtCfg); err != nil {
		log.Fatal(err)
	}

	localDataDir := *dataDir
	if mode == runthrough.ModeRunThrough {
		if rtCfg.CacheDir == "" {
			rtCfg.CacheDir = filepath.Join(localDataDir, "cache")
		}
		if rtCfg.Upstream.Endpoint == "" || rtCfg.Upstream.AccessKey == "" || rtCfg.Upstream.SecretKey == "" {
			log.Fatal("run-through mode requires upstream credentials (set STOW_*, S3_*, or AWS_* env vars)")
		}
	}

	store, adapter, err := buildStore(mode, *backend, localDataDir, rtCfg)
	if err != nil {
		log.Fatal(err)
	}

	storeLimits := nativeStorageLimits{maxBytes: *maxBytes, maxObjects: *maxObjects}
	boundStore, err := bindNativeRuntimeStore(store, *backend, adapter, storeLimits)
	if err != nil {
		log.Fatal(err)
	}
	store = boundStore

	retryCancel, retryDone := startOutboxRetryWorker(adapter)
	defer retryCancel()

	creds := auth.Credentials{}
	if *accessKey != "" && *secretKey != "" {
		creds.AccessKeyID = *accessKey
		creds.SecretAccessKey = *secretKey
	} else {
		generated, err := auth.GenerateCredentials()
		if err != nil {
			log.Fatalf("generate credentials: %v", err)
		}
		creds = generated
	}

	region := strings.TrimSpace(*regionFlag)
	if region == "" {
		region = auth.DefaultRegion
	}
	verifier := auth.NewVerifier(region)
	writePolicy := runthrough.EffectiveWritePolicy(rtCfg)
	cachePolicy := "none"
	upstreamHost := ""
	if mode == runthrough.ModeRunThrough {
		cachePolicy = string(rtCfg.Policy)
		upstreamHost = runthrough.RedactEndpoint(rtCfg.Upstream.Endpoint)
	}
	srv, err := s3api.New(s3api.Config{
		Store:            store,
		Auth:             s3api.SigV4Auth(verifier, creds),
		Host:             *host,
		BaseHost:         strings.TrimSpace(*baseHost),
		Port:             *port,
		DataDir:          localDataDir,
		Region:           region,
		Mode:             string(mode),
		CachePolicy:      cachePolicy,
		WritePolicy:      writePolicy,
		UpstreamHost:     upstreamHost,
		AllowPublicAdmin: *allowPublicAdmin,
		AdminToken:       *adminToken,
		CORSOrigins:      corsOrigins,
	})
	if err != nil {
		log.Fatalf("create server: %v", err)
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ListenAndServe()
	}()

	deadline := time.Now().Add(2 * time.Second)
	for srv.Addr() == "" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	addr := srv.Addr()
	if addr == "" {
		log.Fatal("server failed to bind")
	}

	if *parentPid > 0 {
		armParentWatch(*parentPid)
	}

	endpoint := "http://" + addr
	announceStartup(*readyFd, readyDetails{
		endpoint: endpoint,
		mode:     string(mode),
		backend:  *backend,
		limits:   storeLimits,
		creds:    creds,
		banner:   runthrough.StartupBanner(rtCfg, mode),
		region:   region,
	})

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		fmt.Printf("\nshutting down (%s)...\n", sig)
		retryCancel()
		select {
		case <-retryDone:
		case <-time.After(2 * time.Second):
			log.Printf("retry worker did not stop before shutdown timeout")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			log.Printf("shutdown error: %v", err)
		}
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			log.Fatalf("serve error: %v", err)
		}
	}
}
