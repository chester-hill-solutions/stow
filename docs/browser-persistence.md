# Browser persistence profile

`@chester-hill-solutions/stow-s3/browser` adds a browser-facing persistence coordinator around the
memory-only `EmbeddedStow` WASM profile. It is a separate entry point;
`EmbeddedStow` and `@chester-hill-solutions/stow-s3/node-wasm` keep their existing synchronous
contracts.

The coordinator never treats a successful in-memory mutation as durable until
its adapter commit succeeds. Every mutation is serialized FIFO, applied to the
in-memory runtime, committed as one logical transaction, and only then
resolved to the caller. If a commit fails, the coordinator restores the last
durable generation into the memory runtime and rejects with
`persistence_error`; a failed restore poisons the profile rather than exposing
divergent state.

```ts
import {
  IndexedDbPersistenceAdapter,
  openBrowserEmbeddedStow,
} from "@chester-hill-solutions/stow-s3/browser";

const persistence = new IndexedDbPersistenceAdapter({
  databaseName: "my-app-stow",
});

const stow = await openBrowserEmbeddedStow(wasmHost, {
  namespace: "app-profile",
  persistence,
  maxBytes: 10_000_000,
  maxObjects: 1_000,
});

await stow.createBucket("assets");
await stow.putObject(
  "assets",
  "hello.txt",
  new TextEncoder().encode("hello"),
  { contentType: "text/plain" },
);
// The mutation is committed before this promise resolves.
await stow.close();
```

The IndexedDB adapter uses a manifest store plus bucket and object stores.
Object records use a compound namespace/bucket/key key and store `Uint8Array`
bytes directly. A format-version marker and generation number make stale or
unknown records fail loudly as `persistence_corrupt`. Namespace ownership is
exclusive: a second live opener of the same namespace fails with `already_open`
(the adapter uses Web Locks when available and an in-process equivalent
otherwise). The caller-supplied host is never closed by `close()`.

`reset()` atomically clears the persisted generation and resets the live memory
profile while keeping the instance and its namespace ownership. `close()` is
terminal and idempotent: it drains accepted operations, closes the inner
runtime and persistence resources, and retains the committed data for the next
`openBrowserEmbeddedStow` call. The browser profile reports
`backend: "indexeddb"`, `persistent: true`, `multipart: false`, and
`upstream: false`; it never silently falls back to ephemeral memory.

Applications that need another durable store can implement `PersistenceAdapter`
with `open`, `load`, `commit`, `clear`, and `close`. Commits carry an expected
generation and a list of typed changes, so an adapter can apply all changes in
one transaction and reject stale writers.

## Known limits

- Replay restores object bytes, buckets, metadata, and content types. Creation
  dates, ETags, and last-modified times are persisted when available but are not
  restored exactly, because the inner `EmbeddedStow` replay API is synchronous
  and does not accept them.
- Cross-context (multi-tab) ownership depends on Web Locks. Without Web Locks the
  adapter only prevents a second opener in the same JavaScript realm.
- The repository exercises this profile with a fake host and an in-memory
  transactional adapter. There is no checked-in real-browser integration test, so
  real IndexedDB and real Web Locks behavior is unverified by CI.
- The object store key path is `["namespace", "bucket", "key"]`, so one database
  can hold several namespaces rather than requiring one database per namespace.
- Streaming uploads are unsupported; the host bridge still buffers whole objects.
