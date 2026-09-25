# Stow

Stow is an S3-compatible object store for local development, tests, and short-lived tools or agents.

Applications use Stow to create buckets and upload or download files through the S3 APIs they already use. Local mode stores data on the machine or host where Stow runs. Run-through mode can use an upstream S3-compatible service as a cache or write target.

A **bucket** is a named container. An **object** is a file stored in a bucket, together with metadata such as its content type, size, and ETag.

## What Stow provides

- An S3 HTTP server for local development and tests.
- A TypeScript package for Node.js and AWS SDK v3.
- A Go runtime for in-process, memory-backed storage.
- A WebAssembly runtime for Node.js and browser integrations.
- Filesystem persistence for data that must survive a process restart.
- Read-through caching and explicit upstream write propagation for run-through workflows.
- SigV4 authentication for the S3 endpoint.

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

## Quick start: CLI server

Build the binary:

```bash
make build
```

Start a filesystem-backed server:

```bash
./bin/stow serve --port 0 --data-dir .stow
```

The server prints a machine-readable readiness line:

```text
STOW_READY endpoint=http://127.0.0.1:43127 access_key=... secret_key=... mode=local
```

Use the printed endpoint and credentials with an S3 client. The server supports path-style requests and uses `us-east-1` as its default region.

For a temporary in-memory server:

```bash
./bin/stow serve --port 0 --backend memory
```

## Quick start: TypeScript

Install the package and start a managed server:

```bash
npm install @chs/stow
```

```ts
import {
  GetObjectCommand,
  ListBucketsCommand,
  PutObjectCommand,
  S3Client,
} from "@aws-sdk/client-s3";
import { Stow } from "@chs/stow";

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

  stow "github.com/chester-hill-solutions/stow/pkg/stow"
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
import { EmbeddedStow } from "@chs/stow/embedded";
import { loadNodeWasmHost } from "@chs/stow/node-wasm";

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

Stow keeps bucket namespace operations local. It does not create upstream buckets automatically.

## Running on another host

The server can bind to a network interface:

```bash
./bin/stow serve --host 0.0.0.0 --port 9000
```

Clients on the same network can then connect to the host's port and use S3 operations. Keep the server behind a firewall or private network boundary. Put it behind a TLS-terminating proxy before sending traffic over an untrusted network.

The S3 routes require the generated or configured credentials. Administrative routes are separate from S3 authentication and must not be exposed publicly. Use the current server for local development and controlled private deployments. It is not a hardened public or multi-tenant storage service.

## Development

Run the complete local checks:

```bash
make test-all
make standards
```

The repository contains the Go server, storage backends, runtime packages, WebAssembly bridge, TypeScript package, and shared conformance tests.

## License

Apache License 2.0.
