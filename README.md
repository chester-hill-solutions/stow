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
```

## Modes

| Mode | When |
|------|------|
| `local` | Default when no upstream credentials; force with `--mode local` or `STOW_MODE=local` |
| `readThroughCache` | Auto when `STOW_*` / `S3_*` / `AWS_*` endpoint + keys are set; local writes remain local |
| `mirrorWrites` | Explicit read-through plus upstream propagation; emits a loud startup warning |

Live writes to upstream require `--allow-live-writes` / `STOW_ALLOW_LIVE_WRITES=true`.

See [docs/adr/0001-auto-detect-run-through.md](docs/adr/0001-auto-detect-run-through.md) and [docs/compat-contract.md](docs/compat-contract.md).

## Layout

```
cmd/stow/           CLI
internal/s3api/     S3 HTTP + admin routes
internal/storage/   filesystem + memory stores (JSON sidecars on disk)
internal/auth/      SigV4
internal/runthrough/ upstream adapter
conformance/        AWS SDK Go v2 conformance tests
packages/stow/      @chs/stow TypeScript wrapper
```

## Develop

```bash
make build
make test
make test-conformance
cd packages/stow && npm test
```

License: Apache 2.0
