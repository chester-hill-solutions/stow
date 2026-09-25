package main

import (
	"context"
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

	"github.com/chester-hill-solutions/stow/internal/auth"
	"github.com/chester-hill-solutions/stow/internal/runthrough"
	"github.com/chester-hill-solutions/stow/internal/s3api"
	"github.com/chester-hill-solutions/stow/internal/storage"
	"github.com/chester-hill-solutions/stow/internal/storage/fs"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "serve":
		serve(os.Args[2:])
	default:
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, "usage: stow <command>\n\ncommands:\n  serve    start the S3-compatible server\n")
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
	if mode == runthrough.ModeRunThrough && backend == "memory" && (config.Policy == runthrough.PolicyMirrorWrites || config.AllowLiveWrites) {
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
	allowPublicAdmin := flags.Bool("allow-public-admin", false, "Allow unauthenticated admin and metrics routes on non-loopback requests")
	modeFlag := flags.String("mode", "auto", "Operational mode: local, run-through, or auto (default)")
	allowLiveWrites := flags.Bool("allow-live-writes", false, "Propagate writes to upstream S3")
	cacheDir := flags.String("cache-dir", "", "Run-through cache directory (default: <data-dir>/cache)")
	cacheMaxBytes := flags.Int64("cache-max-bytes", -1, "Maximum separate cache bytes (0 disables the limit; -1 uses environment)")
	cacheMaxObjects := flags.Int64("cache-max-objects", -1, "Maximum separate cache objects (0 disables the limit; -1 uses environment)")
	cacheTTL := flags.Duration("cache-ttl", -1, "Separate cache entry lifetime (0 disables expiry; -1 uses environment)")
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
		log.Printf("WARNING: admin and metrics routes are exposed without authentication")
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

	var localStore storage.Store
	var err error
	switch *backend {
	case "filesystem":
		localStore, err = fs.NewFilesystemStore(localDataDir)
	case "memory":
		localStore = storage.NewMemoryStore()
	default:
		log.Fatalf("invalid --backend %q (want filesystem or memory)", *backend)
	}
	if err != nil {
		log.Fatalf("open store: %v", err)
	}

	var store storage.Store = localStore
	var adapter *runthrough.Adapter
	if mode == runthrough.ModeRunThrough {
		var cacheStore storage.Store
		if *backend == "filesystem" {
			cacheStore, err = fs.NewFilesystemStore(rtCfg.CacheDir)
		} else {
			cacheStore = storage.NewMemoryStore()
		}
		if err != nil {
			log.Fatalf("open cache store: %v", err)
		}
		upstreamClient, err := runthrough.NewS3Client(rtCfg.Upstream)
		if err != nil {
			log.Fatalf("upstream client: %v", err)
		}
		outbox, err := runthrough.NewFileOutbox(filepath.Join(rtCfg.CacheDir, "outbox.json"))
		if err != nil {
			log.Fatalf("open outbox: %v", err)
		}
		adapter = runthrough.NewWithOutbox(rtCfg, localStore, cacheStore, upstreamClient, outbox)
		if err := adapter.RecoverPrepared(context.Background()); err != nil {
			log.Fatalf("recover outbox: %v", err)
		}
		store = adapter
	}
	store, err = bindNativeRuntimeStore(store, *backend, adapter)
	if err != nil {
		log.Fatal(err)
	}

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

	verifier := auth.NewVerifier(auth.DefaultRegion)
	writePolicy := "local-only"
	if rtCfg.Policy == runthrough.PolicyMirrorWrites {
		writePolicy = "mirrorWrites"
	} else if rtCfg.AllowLiveWrites {
		writePolicy = "allowLiveWrites"
	}
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
		Region:           auth.DefaultRegion,
		Mode:             string(mode),
		CachePolicy:      cachePolicy,
		WritePolicy:      writePolicy,
		UpstreamHost:     upstreamHost,
		AllowPublicAdmin: *allowPublicAdmin,
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

	endpoint := "http://" + addr
	modeStr := string(mode)
	fmt.Print(runthrough.StartupBanner(rtCfg, mode))
	fmt.Printf("stow listening on %s (mode=%s)\n", endpoint, modeStr)
	fmt.Printf("STOW_READY endpoint=%s access_key=%s secret_key=%s mode=%s\n", endpoint, creds.AccessKeyID, creds.SecretAccessKey, modeStr)

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
