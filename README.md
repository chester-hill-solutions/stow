# Stow

Docker-free, S3-compatible bucket service for development and tests. PGLite-inspired: install a package, start an endpoint, point the AWS SDK at it.

**Scope:** local and run-through caching for apps — not production object storage.

Stow targets the version-pinned AWS SDK v3 and AWS SDK for Go v2 behavior. It preserves local safety boundaries even where SDKs are more permissive than Amazon service documentation.

## Quick start (CLI)

```bash
make build
./bin/stow serve --port 0 --data-dir .stow
```

Parse the `STOW_READY` line for `endpoint`, `access_key`, and `secret_key`. Create buckets with the AWS SDK (path-style, region `us-east-1`).

## Quick start (TypeScript)

```bash
make build
cd packages/stow && npm install && npm run build
```

```ts
import { Stow } from "@chs/stow";

const stow = await Stow.start({
  dataDir: ".stow",
  buckets: ["uploads"],
  port: 0,
});

const client = new S3Client(stow.awsSdkV3Config());
await stow.stop();

// For an endpoint owned by another process:
const connection = Stow.connect({
  endpoint: "http://127.0.0.1:9000",
  accessKeyId: "access",
  secretAccessKey: "secret",
});
connection.client.send(command); // connection owns and destroys this client
connection.disconnect();
```

## Embedded Go runtime

The native `stow serve` path binds its selected local/run-through store to the internal runtime facade before exposing the S3 HTTP adapter. This keeps the public embedded runtime small while making native lifecycle, quota, and reset behavior pass through the same instance boundary. The direct runtime remains an in-process, memory-only profile for Go callers; it does not start an HTTP server or use AWS credentials:

```go
package main

import (
  "context"
  "github.com/chester-hill-solutions/stow/pkg/stow"
)

func main() {
  runtime, err := stow.Open(stow.Options{MaxBytes: 10 << 20, MaxObjects: 1000})
  if err != nil { panic(err) }
  defer runtime.Close()
  ctx := context.Background()
  if err := runtime.CreateBucket(ctx, "assets"); err != nil { panic(err) }
  if _, err := runtime.PutObject(ctx, "assets", "hello.txt", []byte("hello"), stow.PutOptions{}); err != nil { panic(err) }
}
```

Object listings return an explicit page. Follow `NextCursor` when `Truncated` is true instead of assuming the default page is complete:

```go
page, err := runtime.ListObjects(ctx, "assets", stow.ListOptions{Limit: 100})
for {
  // use page.Objects
  if !page.Truncated {
    break
  }
  page, err = runtime.ListObjects(ctx, "assets", stow.ListOptions{Limit: 100, Cursor: page.NextCursor})
}
```

The `js/wasm` bridge exposes the same memory runtime through a versioned JSON/base64 host protocol. The Node package includes the tested WASM asset and loader:

```ts
import { EmbeddedStow } from "@chs/stow/embedded";
import { loadNodeWasmHost } from "@chs/stow/node-wasm";

const host = await loadNodeWasmHost();
const embedded = EmbeddedStow.open(host);
// use embedded...
embedded.close();
await host.close();
```

```sh
make test-wasm
```

The native S3 endpoint and the TypeScript `Stow.start()` / `Stow.connect()` contracts remain unchanged.

## Modes and policies

| Mode | When |
|------|------|
| `local` | Default when no upstream credentials; force with `--mode local` or `STOW_MODE=local` |
| `run-through` | Auto when `STOW_*` / `S3_*` / `AWS_*` endpoint + keys are set |

| Policy | Behavior |
|--------|----------|
| `readThroughCache` | Read upstream on local cache misses; local writes remain local unless live writes are explicitly enabled |
| `mirrorWrites` | Read-through plus durable upstream propagation; emits a loud startup warning |

Live writes to upstream require `--allow-live-writes` / `STOW_ALLOW_LIVE_WRITES=true`, the filesystem backend, and a durable coordinated outbox. A prepared local-mutation intent is persisted before the local record changes; startup/retry reconciliation commits or discards it using the immutable object version. File-backed workers use expiring per-entry claims and a filesystem lock so separate processes do not concurrently propagate the same key. An entry that was already attempted is checked against upstream before it is replayed, so a crash after an upstream success but before the acknowledgement is normally acknowledged rather than duplicated; the exception is an upstream that cannot be read during recovery or an object another writer has since replaced. `MemoryOutbox` and other uncoordinated outboxes are rejected before a live mutation touches local storage. Every process sharing an outbox file must run the same Stow revision: a file written by a newer schema is rejected at open rather than partially upgraded.

Run-through cache limits can be set with `--cache-max-bytes`, `--cache-max-objects`, and `--cache-ttl`, or with `STOW_CACHE_MAX_BYTES`, `STOW_CACHE_MAX_OBJECTS`, and `STOW_CACHE_TTL`. Cache entries are bounded by LRU and optionally expire after the configured TTL.

See [docs/adr/0001-auto-detect-run-through.md](docs/adr/0001-auto-detect-run-through.md), [docs/adr/0003-embedded-runtime.md](docs/adr/0003-embedded-runtime.md), and [docs/compat-contract.md](docs/compat-contract.md).

## Layout

```
cmd/stow/           CLI
internal/s3api/     S3 HTTP + admin routes
internal/storage/   shared storage contract + memory store
internal/storage/fs/ filesystem backend (atomic JSON object records on disk)
internal/auth/      SigV4
internal/runthrough/ upstream adapter
pkg/stow/            direct embedded Go runtime
cmd/stow-wasm/       js/wasm host bridge
wasm/                Node-hosted WASM tests
conformance/         AWS SDK Go v2 conformance tests
packages/stow/      @chs/stow TypeScript wrapper
```

## Develop

```bash
make test-all
make standards
```

`make test-node` and `make test-wasm` also rebuild the tested package/WASM artifacts. The release workflow publishes the exact npm tarball produced after these gates.

License: Apache 2.0
