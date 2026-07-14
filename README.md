# Stow

Docker-free, S3-compatible bucket service for development and tests. PGLite-inspired: install a package, start an endpoint, point the AWS SDK at it.

**Scope:** local and run-through caching for apps — not production object storage.

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

## Install (consumers)

Published as `@chs/stow` **v0.1.0** (GitHub Packages when available). Prebuilt binaries ship on the matching GitHub Release:

| Asset | Platforms |
|-------|-----------|
| `stow-darwin-arm64` / `stow-darwin-amd64` | macOS |
| `stow-linux-amd64` / `stow-linux-arm64` | Linux |
| `chs-stow-0.1.0.tgz` | npm pack of `@chs/stow` |

`Stow.start()` resolves the binary in this order:

1. `STOW_BIN`
2. Package-local `bin/stow` (auto-downloaded from the release when missing)
3. Monorepo `bin/stow` after `make build`
4. `stow` on `PATH`

Private-repo downloads need `GH_TOKEN` / `GITHUB_TOKEN` (or `STOW_GITHUB_TOKEN`).

Until GitHub Packages publish is available:

```bash
# From a release asset, or local pack:
npm install ./chs-stow-0.1.0.tgz
# or workspace / file: link to packages/stow
```

## Modes

| Mode | When |
|------|------|
| `local` | Default when no upstream credentials; force with `--mode local` or `STOW_MODE=local` |
| `run-through` | Auto when `STOW_*` / `S3_*` / `AWS_*` endpoint + keys are set; policy `readThroughCache` |

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
