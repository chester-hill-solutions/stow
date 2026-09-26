# Stow

Stow is an S3-compatible object store for local development, tests, and short-lived tools or agents.

Applications use Stow to create buckets and upload or download files through the S3 APIs they already use. Local mode stores data on the machine or host where Stow runs. Run-through mode can use an upstream S3-compatible service as a cache or write target.

A **bucket** is a named container. An **object** is a file stored in a bucket, together with metadata such as its content type, size, and ETag.

## What Stow provides

- An S3 HTTP server for local development and tests.
- A scoped session API for TypeScript and Python: one call starts a private
  server, hands back a ready S3 client, and cleans everything up on close.
- A Go runtime for in-process, memory-backed storage.
- A WebAssembly runtime for Node.js and browser integrations.
- Filesystem persistence for data that must survive a process restart.
- Read-through caching and explicit upstream write propagation for run-through workflows.
- SigV4 authentication for the S3 endpoint.
- `stow doctor`, a diagnostic that reports both the client and server sides of an
  installation.

Stow implements the common S3 operations needed by application and test workloads:

- create, inspect, list, and delete buckets;
- put, get, head, copy, and delete objects;
- list objects with prefixes and pagination;
- range reads;
- conditional writes and reads;
- MD5, CRC32, CRC32C, SHA-1, and SHA-256 checksums;
- multipart uploads;
- presigned requests.

Stow implements a subset of Amazon S3. Versioning, ACLs, bucket policies, lifecycle rules, replication, notifications, tagging, object lock, S3 Select, and KMS-backed encryption are not implemented.

## When to use it

Use Stow when a workload needs S3 behavior without a cloud account or network service:

- local application development;
- unit and integration tests that need a real S3-shaped API;
- CI jobs that need disposable object storage;
- tools and agents that need a private scratch bucket;
- Go programs that want an in-process object runtime;
- browser or Node.js integrations that need an embedded runtime;
- development against an existing upstream S3-compatible service.

## Install

**The npm and PyPI packages are not published yet.** Both names are reserved and
the release pipeline that will publish them is built and green, but `@chester-hill-solutions/stow-s3`
is not on npm and `stow-s3` is not on PyPI. `npm install @chester-hill-solutions/stow-s3` and
`pip install stow-s3` will fail with a 404 today.

**The npm package will live on GitHub Packages, not npmjs.org, so the bare
install command will not work when it is published.** This organisation owns
nothing on npmjs — the scope does not exist there — and a scope-specific
registry in `.npmrc` overrides `--registry`, so npm will silently resolve against
the wrong host and report a 404 that looks like a naming problem. Route the scope
explicitly, and commit that line so it cannot be forgotten:

```ini
# .npmrc
@chester-hill-solutions:registry=https://npm.pkg.github.com
```

Then `npm install @chester-hill-solutions/stow-s3` resolves. PyPI has no such
step. This is recorded now, while the packages are still private, so the publish
step is a visibility flip rather than a documentation change under time pressure.

The Go module is published and installs today:

```bash
go get github.com/chester-hill-solutions/stow-s3/pkg/stow
```

TypeScript and Python work from a source checkout:

```bash
git clone https://github.com/chester-hill-solutions/stow-s3
cd stow-s3
make build                        # bin/stow-s3 for your platform
export STOW_BIN="$PWD/bin/stow-s3"

# TypeScript client
cd packages/stow-s3 && npm install && npm run build && cd ../..

# Python client
pip install -e "packages/stow-s3-py[boto3]"
```

`STOW_BIN` tells a client which server binary to run. A published package carries
its own binary for your platform and does not need it. `stow-s3 doctor` reports
which side is broken if the binary cannot be found or run.

## Quick start: a scoped session

A session is the shortest path from nothing to a working S3 client. It starts a
private server on an ephemeral port, creates a bucket, waits until the server
reports itself ready, and hands back a client that is already pointed at it. On
close it stops the server and removes the data directory.

TypeScript (not yet on npm — see [Install](#install)):

```bash
# from a source checkout
make build && export STOW_BIN="$PWD/bin/stow-s3"
cd packages/stow-s3 && npm install && npm run build
```

```ts
import { GetObjectCommand, PutObjectCommand } from "@aws-sdk/client-s3";
import { withStow } from "@chester-hill-solutions/stow-s3";

const body = await withStow(async (session) => {
  await session.s3.send(new PutObjectCommand({
    Bucket: session.bucket,
    Key: "input.json",
    Body: '{"task":"summarize"}',
  }));

  const fetched = await session.s3.send(new GetObjectCommand({
    Bucket: session.bucket,
    Key: "input.json",
  }));
  return fetched.Body?.transformToString();
});
```

`withStow` closes the session even if the callback throws, and reports both
errors if the close also fails. Use `openStow` when you need the session to
outlive a single callback.

Python (not yet on PyPI — see [Install](#install)):

```bash
make build && export STOW_BIN="$PWD/bin/stow-s3"
pip install -e "packages/stow-s3-py[boto3]"
```

```python
from stow_s3 import with_session

with with_session() as session:
    s3 = session.s3_client()
    bucket = session.new_bucket_name()
    s3.create_bucket(Bucket=bucket)
    s3.put_object(Bucket=bucket, Key="input.json", Body=b'{"task":"summarize"}')
    body = s3.get_object(Bucket=bucket, Key="input.json")["Body"].read()
```

The two clients differ in one deliberate way. The TypeScript session creates its
bucket for you and exposes it as `session.bucket`. The Python session performs
no S3 I/O at all: it hands back a client, and `new_bucket_name()` returns a name
that is very unlikely to collide with another session. Creating the bucket is
yours to do, which is what keeps `boto3` a genuinely optional extra.

### What a session guarantees

- **It cannot inherit your cloud configuration.** `STOW_*`, `S3_*`, and `AWS_*`
  are stripped from the child environment before anything is set, so a stray
  credential in your shell cannot turn a local session into a run-through one.
- **It cannot outlive you.** A session passes its parent's process ID, and the
  server exits if that process dies, including when it is killed rather than
  closed.
- **It reports the server's real limits, not assumed ones.** Capabilities and
  limits come from the readiness message.
- **It is bounded.** The default session holds at most 16 MiB and 1,000 objects,
  and a single request body is capped at 8 MiB. The limits are enforced by the
  server on every request, not by the client.
- **It is tuned for many-per-machine use.** A session's own server runs with a
  tighter garbage collector target than a long-lived server, because a session's
  peak memory matters more than its throughput. Set `GOGC` yourself to override
  it. A server you start with `stow serve` is never tuned this way.

## Diagnostics: stow doctor

When a session will not start, `stow doctor` reports what each side can see
rather than a single opaque failure. It checks both the client and the server and
merges the results into one list:

```bash
npx stow-doctor
npx stow-doctor --json
```

It never prints a credential. Checks that only apply in some setups are reported
as warnings rather than failures, so a local-only user is not failed for having
no upstream S3.

`npx stow-doctor` exits 0 when everything passed and 1 when a required check
failed. The `stow doctor` subcommand on the server side uses a third code, 2, for
a check that could not be run at all, so a broken installation is
distinguishable from a healthy one with a failing optional check.

## Quick start: CLI server

Build the binary:

```bash
make build
```

Start a filesystem-backed server:

```bash
./bin/stow-s3 serve --port 0 --data-dir .stow
```

The server prints a machine-readable readiness line:

```text
STOW_READY endpoint=http://127.0.0.1:43127 access_key=... secret_key=... mode=local
```

Use the printed endpoint and credentials with an S3 client. The server supports path-style requests and uses `us-east-1` as its default region.

A hand-run server keeps running when its shell exits, which is usually what you
want. To make it exit when a specific parent process dies instead, pass
`--parent-pid`:

```bash
./bin/stow-s3 serve --port 0 --parent-pid 12345
```

This is opt-in for that reason. A session sets it for you; see
[Quick start: a scoped session](#quick-start-a-scoped-session).

The `STOW_READY` line above is the simple channel. Sessions use a stricter,
versioned channel that carries capabilities and limits as JSON over an inherited
file descriptor, so credentials never appear on a log. Both clients reject a
protocol version they do not speak rather than starting a half-working session.

For a temporary in-memory server:

```bash
./bin/stow-s3 serve --port 0 --backend memory
```

## Quick start: TypeScript

Start a managed server (see [Install](#install) for why this is not yet an
`npm install`):

```bash
export STOW_BIN="$PWD/bin/stow-s3"
```

```ts
import {
  GetObjectCommand,
  ListBucketsCommand,
  PutObjectCommand,
  S3Client,
} from "@aws-sdk/client-s3";
import { Stow } from "@chester-hill-solutions/stow-s3";

const stow = await Stow.start({
  backend: "memory",
  buckets: ["uploads"],
  port: 0,
});

try {
  const s3 = new S3Client(stow.awsSdkV3Config());

  await s3.send(new PutObjectCommand({
    Bucket: "uploads",
    Key: "hello.txt",
    Body: "hello",
  }));

  const response = await s3.send(new GetObjectCommand({
    Bucket: "uploads",
    Key: "hello.txt",
  }));

  console.log(await response.Body?.transformToString());
} finally {
  await stow.stop();
}
```

`Stow.start()` starts the `stow` executable, waits until it is ready, creates the requested buckets, and returns an AWS SDK configuration. When the executable is not in the repository's `bin/` directory, place `stow` on `PATH` or set `STOW_BIN`.

`Stow.start()` is the long-lived form: the server keeps running until you stop
it, and it uses the server's default collector settings. For a scoped,
throwaway bucket that cleans up after itself, use
[`withStow`](#quick-start-a-scoped-session) instead.

Use `Stow.connect()` when an S3 endpoint is already running:

```ts
const connection = Stow.connect({
  endpoint: "http://127.0.0.1:9000",
  accessKeyId: "access",
  secretAccessKey: "secret",
});

try {
  await connection.client.send(new ListBucketsCommand({}));
} finally {
  connection.disconnect();
}
```

## Embedded Go runtime

The public Go package provides an in-process, memory-backed runtime. It does not start an HTTP listener and does not require AWS credentials:

```go
package main

import (
  "context"
  "log"

  stow "github.com/chester-hill-solutions/stow-s3/pkg/stow"
)

func main() {
  runtime, err := stow.Open(stow.Options{
    Backend:    stow.BackendMemory,
    MaxBytes:   10 << 20,
    MaxObjects: 1000,
  })
  if err != nil {
    log.Fatal(err)
  }
  defer runtime.Close()

  ctx := context.Background()
  if err := runtime.CreateBucket(ctx, "assets"); err != nil {
    log.Fatal(err)
  }

  if _, err := runtime.PutObject(ctx, "assets", "hello.txt", []byte("hello"), stow.PutOptions{
    ContentType: "text/plain",
  }); err != nil {
    log.Fatal(err)
  }
}
```

## Node.js, WebAssembly, and browser profiles

The Node.js WebAssembly profile provides an in-memory object runtime without an HTTP server:

```ts
import { EmbeddedStow } from "@chester-hill-solutions/stow-s3/embedded";
import { loadNodeWasmHost } from "@chester-hill-solutions/stow-s3/node-wasm";

const host = await loadNodeWasmHost();
const embedded = EmbeddedStow.open(host);

try {
  embedded.createBucket("assets");
  embedded.putObject("assets", "hello.txt", new TextEncoder().encode("hello"));
  const object = embedded.getObject("assets", "hello.txt");
  console.log(object.data);
} finally {
  embedded.close();
  await host.close();
}
```

The browser profile requires a compatible embedded host and persistence adapter. It can store snapshots in IndexedDB and restore them when the profile opens again.

## Run-through mode

Local mode keeps all object data in the selected local backend. Run-through mode adds an upstream S3 client and a separate cache.

In run-through mode, local data is authoritative. Reads can fall back to an existing upstream bucket. Upstream configuration comes from `STOW_*`, `S3_*`, or `AWS_*` environment variables, or from the corresponding command-line options.

Live upstream writes require all of the following:

- run-through mode;
- the filesystem backend;
- explicit live-write opt-in with `--allow-live-writes` or `STOW_ALLOW_LIVE_WRITES=true`;
- a durable coordinated outbox.

`STOW_POLICY=mirrorWrites` is **not** that opt-in. The policy chooses how reads
are routed; the flag is the consent to mutate a real provider, and only the flag
grants it. Setting the policy alone leaves every write local. An explicit
`STOW_ALLOW_LIVE_WRITES=false` is a refusal that the policy cannot override.
See [ADR 0005](docs/adr/0005-live-write-requires-explicit-consent.md).

Stow keeps bucket namespace operations local. It does not create upstream buckets automatically.

## Resetting a data directory

`Stow.start({ resetOwnedData: true })` deletes the data directory before starting.
It deletes only a directory stow created, identified by a `.stow-owner` marker
the server writes on startup. A directory without that marker is refused, as are
the filesystem root, your home directory, the working directory, and any ancestor
of them — so `dataDir: ".."` cannot reach your home. A directory that does not
exist is not an error, so resetting before a first run is fine.

`cleanSlate` is a deprecated alias with identical checks. A data directory
created before this check existed has no marker, so its first reset is refused;
nothing is deleted. See
[ADR 0006](docs/adr/0006-owned-data-directory-reset.md).

## Running on another host

The server can bind to a network interface:

```bash
./bin/stow-s3 serve --host 0.0.0.0 --port 9000
```

Clients on the same network can then connect to the host's port and use S3 operations. Keep the server behind a firewall or private network boundary. Put it behind a TLS-terminating proxy before sending traffic over an untrusted network.

The S3 routes require the generated or configured credentials. Use the current server for local development and controlled private deployments. It is not a hardened public or multi-tenant storage service.

## Admin and metrics routes

Diagnostics live under `/_stow/`, separate from S3 authentication:

| Route | Effect |
|---|---|
| `/_stow/health` | liveness |
| `/_stow/status` | mode, policies, version |
| `/_stow/inspect` | object and upload state |
| `/_stow/metrics` | request and quota counters |
| `/_stow/outbox/retry` | retries failed upstream propagation |
| `/_stow/outbox/discard` | discards an outbox entry |

The two `outbox` routes change what is propagated to a live provider, so they
always require the admin token — including on loopback, because loopback is not
a privilege boundary. The read-only routes work on loopback without a token, so
`stow doctor` and a local shell need nothing, and require the token from any
other address.

Set the token with `STOW_ADMIN_TOKEN` rather than the flag, so it does not appear
in the process list:

```bash
STOW_ADMIN_TOKEN=$(openssl rand -hex 24) ./bin/stow-s3 serve --host 0.0.0.0
curl -H "X-Stow-Admin: $STOW_ADMIN_TOKEN" http://host:9000/_stow/metrics
```

A request without a valid token gets `404`, not `403`, so the route's existence
is not advertised. With no token configured the outbox routes are not reachable
at all.

`--allow-public-admin` is **deprecated and ignored**. It used to be the only
gate, which made exposing the outbox routes an unauthenticated act rather than a
privileged one. The flag now logs a warning and changes nothing; use
`--admin-token`.

## Browser origins (CORS)

Stow permits loopback origins by default — `http://localhost:*` and
`http://127.0.0.1:*` — which is the case a local dev server actually has. It
previously reflected whatever `Origin` a request carried, which let any website
a developer visited read their local bucket using the credentials their SDK had
already placed in the page.

Name the origins you need instead:

```bash
./bin/stow-s3 serve --cors-origin https://app.example --cors-origin http://localhost:5173
```

`--cors-origin *` restores the old permissive behavior, but only when you ask
for it by name. Responses always carry `Vary: Origin`, without which a cache can
serve one origin's allow header to another.

## Development

Run the complete local checks:

```bash
make test-all
make standards
```

`make test-all` covers the Go server, the shared conformance suite across every
backend, the TypeScript client, the Python client, and the WebAssembly bridge.
`make standards` runs the quality ratchets; these are floors, so an improvement is
reported rather than failed and a regression fails the build.

The Go version is pinned exactly, in `.go-version` and in the `toolchain` line of
`go.mod`. The WebAssembly artifact in `packages/stow-s3/dist` is committed and
`check-generated` rebuilds and diffs it, and a build from a different patch
release does not reproduce it byte for byte. Nothing has to be installed by hand:
the `toolchain` directive makes the Go command fetch the pinned version on
demand. To move to a new patch, change both files, then rebuild and commit the
artifact.

The repository contains the Go server, storage backends, runtime packages, WebAssembly bridge, TypeScript package, Python package, and shared conformance tests.

Measure session memory with the benchmark. It is deliberately not part of
`make standards`, because it is a measurement tool rather than a check:

```bash
make build
node packages/stow-s3/scripts/benchmark-session.mjs --sweep
```

Rebuild before measuring. A benchmark run against a stale binary reports the
previous build's numbers without saying so. The results, including several
findings that contradicted an earlier theory about what drives session memory,
are recorded in `docs/benchmarks/session-baseline.md`.

## License

Apache License 2.0.
