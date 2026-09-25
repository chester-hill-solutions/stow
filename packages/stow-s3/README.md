# @chs/stow-s3

TypeScript client for the [stow](https://github.com/chester-hill-solutions/stow-s3) local S3-compatible dev bucket service.

## Install

```sh
npm install @chs/stow-s3
```

Build the Go binary first when working from the monorepo:

```sh
make build
```

## Usage

```ts
import { Stow } from "@chs/stow-s3";

const bucket = await Stow.start({
  dataDir: ".dev-bucket",
  buckets: ["uploads"],
  port: 0,
  backend: "filesystem",
});

process.env.S3_ENDPOINT = bucket.endpoint;
process.env.S3_ACCESS_KEY_ID = bucket.accessKeyId;
process.env.S3_SECRET_ACCESS_KEY = bucket.secretAccessKey;

await bucket.stop();
```

`Stow.start()` owns the child process it launches. Use `backend: "memory"` for an explicitly ephemeral instance. To use an already-running endpoint, call `Stow.connect({ endpoint, accessKeyId, secretAccessKey, region })`; its `client` is an owned AWS SDK client that the caller can use directly, and `disconnect()` destroys it. The TypeScript entry points remain endpoint-based; the repository's `js/wasm` bridge is a separate additive profile.

Both connection helpers also accept an optional `sessionToken` or credential `provider` for temporary AWS credentials. Run-through starts can set `cacheMaxBytes`, `cacheMaxObjects`, and `cacheTtlSeconds`; the CLI also reads `STOW_CACHE_MAX_BYTES`, `STOW_CACHE_MAX_OBJECTS`, and `STOW_CACHE_TTL`. Live mirror writes require the filesystem backend and a durable coordinated outbox; file-backed workers use expiring claims to coordinate separate processes, while the package never treats an in-memory outbox as durable.

### Embedded host profile

`EmbeddedStow` is a host-neutral typed wrapper for the memory-only WASM bridge. The host loads the `js/wasm` artifact and provides a synchronous `call(request)` method; the wrapper handles object bytes, metadata, capabilities, quotas, reset, and close:

```ts
import { EmbeddedStow } from "@chs/stow-s3/embedded";

const embedded = EmbeddedStow.open(wasmHost, { maxBytes: 10_000_000 });
embedded.createBucket("assets");
embedded.putObject("assets", "hello.txt", new TextEncoder().encode("hello"));
const object = embedded.getObject("assets", "hello.txt");
embedded.close();
```

The Node package includes the tested WASM asset and a ready-made host loader:

```ts
import { EmbeddedStow } from "@chs/stow-s3/embedded";
import { loadNodeWasmHost } from "@chs/stow-s3/node-wasm";

const host = await loadNodeWasmHost();
const embedded = EmbeddedStow.open(host, { maxBytes: 10_000_000 });
// use embedded...
embedded.close();
await host.close();
```

Listings are cursor-based as well:

```ts
let page = embedded.listObjects("assets", { limit: 100 });
const objects = [];
for (;;) {
  objects.push(...page.objects);
  if (!page.truncated) break;
  page = embedded.listObjects("assets", { limit: 100, cursor: page.nextCursor });
}
```

Run `make test-wasm` or `make test-node` to rebuild the packaged asset. Custom hosts may still provide their own synchronous `call(request)` implementation.

### Scoped sessions

`withStow` gives a short-lived execution context a private, disposable S3
workspace with no lifecycle glue: a local memory backend, a generated bucket and
credentials, a loopback endpoint, and cleanup on success, failure, or
cancellation.

```ts
import { withStow } from "@chs/stow-s3";

const summary = await withStow(async (session) => {
  await session.s3.send(
    new PutObjectCommand({ Bucket: session.bucket, Key: "input.json", Body: body }),
  );
  const input = await session.s3.send(
    new GetObjectCommand({ Bucket: session.bucket, Key: "input.json" }),
  );
  return summarize(await input.Body.transformToString());
});
```

- The native binary is installed automatically as a platform optional package, so
  a plain `npm install` works with no `PATH` setup. Measured in
  [`docs/distribution-spike.md`](../../docs/distribution-spike.md).
- `STOW_*`, `S3_*`, and `AWS_*` are stripped from the child environment, so
  ambient cloud configuration cannot turn a local session into a run-through one.
- Defaults are 16 MiB and 1,000 objects, sized from the measured memory profile in
  [`docs/benchmarks/session-baseline.md`](../../docs/benchmarks/session-baseline.md).
  Override with `maxBytes` and `maxObjects`.
- `capabilities()` reports what the server is actually enforcing, parsed from the
  versioned readiness message. A limit of `0` means the server reported no limit.
- `handoff()` returns a fresh five-key environment mapping for a child process. It
  never mutates the parent environment and includes nothing else.
- `close()` is terminal and idempotent. A failed callback is rethrown even if
  cleanup also fails, and cleanup removes only a directory the session created.

`openStow` is the same thing without the callback, for manual lifetime control.
`Stow.start()`, `Stow.connect()`, and `EmbeddedStow` are unchanged and remain the
advanced process-owned and external-endpoint APIs.

### Browser persistence profile

The additive [`@chs/stow-s3/browser`](../../docs/browser-persistence.md) entry
point coordinates durable commits around the embedded memory profile. The
`openBrowserEmbeddedStow` wrapper replays the committed generation during async
open, serializes operations FIFO, and commits each mutation before its promise
resolves. `IndexedDbPersistenceAdapter` uses a manifest, bucket store, and
object store with direct `Uint8Array` values; it also enforces exclusive
namespace ownership:

```ts
import {
  IndexedDbPersistenceAdapter,
  openBrowserEmbeddedStow,
} from "@chs/stow-s3/browser";

const persistence = new IndexedDbPersistenceAdapter({
  databaseName: "my-app-stow",
});
const embedded = await openBrowserEmbeddedStow(wasmHost, {
  namespace: "app-profile",
  persistence,
  maxBytes: 10_000_000,
});
await embedded.createBucket("assets");
await embedded.putObject("assets", "hello.txt", new TextEncoder().encode("hello"));
await embedded.close();
```

`reset()` clears the persisted generation and live memory state while retaining
ownership. `close()` is terminal, idempotent, drains accepted operations, and
retains data; it does not close the caller-supplied host. Unsupported formats,
stale generations, denied ownership, quota overflow, and closed profiles use
stable persistence error codes rather than falling back to memory-only state.
The existing `EmbeddedStow` and `@chs/stow-s3/node-wasm` contracts remain
unchanged.

CLI equivalent:

```sh
stow serve --data-dir .dev-bucket --port 9000
```

## Binary resolution

1. `STOW_BIN` environment variable
2. `bin/stow-s3` relative to the monorepo root (when developing in-repo)
3. `stow` on `PATH`

## Development

```sh
cd packages/stow-s3
npm install
npm run build
npm test
```

The package test suite includes lifecycle and shared-corpus coverage. Run `make build` before `npm test` when working from the monorepo.
