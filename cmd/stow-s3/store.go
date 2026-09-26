package main

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/chester-hill-solutions/stow-s3/internal/runthrough"
	"github.com/chester-hill-solutions/stow-s3/internal/runtime"
	"github.com/chester-hill-solutions/stow-s3/internal/storage"
)

// buildStore returns the object store a server should serve, plus the
// run-through adapter when one was built.
//
// Local mode returns the plain local store and a nil adapter without ever
// constructing an upstream client. That is the whole safety argument for
// `--mode local`: a local server holds no object capable of reaching a provider,
// so it cannot make an upstream request regardless of what STOW_*, S3_*, or
// AWS_* say. Keeping that construction inside this one branch is what makes it
// checkable, so it is asserted in TestLocalModeBuildsNoUpstreamPath rather than
// left as a claim in a comment.
func buildStore(mode runthrough.Mode, backend runtime.Backend, dataDir string, cfg runthrough.Config) (storage.Store, *runthrough.Adapter, error) {
	localStore := openLocalStore(backend, dataDir)
	if mode != runthrough.ModeRunThrough {
		return localStore, nil, nil
	}

	cacheStore := openLocalStore(backend, cfg.CacheDir)
	upstreamClient, err := runthrough.NewS3Client(cfg.Upstream)
	if err != nil {
		return nil, nil, fmt.Errorf("upstream client: %w", err)
	}
	outbox, err := runthrough.NewFileOutbox(filepath.Join(cfg.CacheDir, "outbox.json"))
	if err != nil {
		return nil, nil, fmt.Errorf("open outbox: %w", err)
	}
	adapter := runthrough.NewWithOutbox(cfg, localStore, cacheStore, upstreamClient, outbox)
	if err := adapter.RecoverPrepared(context.Background()); err != nil {
		return nil, nil, fmt.Errorf("recover outbox: %w", err)
	}
	return adapter, adapter, nil
}
