# @chs/stow

TypeScript client for the [stow](https://github.com/chester-hill-solutions/stow) local S3-compatible dev bucket service.

## Install

```sh
npm install @chs/stow
# or from a GitHub Release tarball:
# npm install ./chs-stow-0.1.0.tgz
```

Platform binaries ship on the matching GitHub Release (`stow-darwin-arm64`, `stow-linux-amd64`, …). `Stow.start()` auto-downloads into the package `bin/` directory when `STOW_BIN` is unset. Private repos need `GH_TOKEN` / `GITHUB_TOKEN`.

## Usage

```ts
import { Stow } from "@chs/stow";

const bucket = await Stow.start({
  dataDir: ".dev-bucket",
  buckets: ["uploads"],
  port: 0,
});

process.env.S3_ENDPOINT = bucket.endpoint;
process.env.S3_ACCESS_KEY_ID = bucket.accessKeyId;
process.env.S3_SECRET_ACCESS_KEY = bucket.secretAccessKey;

await bucket.stop();
```

CLI equivalent:

```sh
stow serve --data-dir .dev-bucket --port 9000
```

## Binary resolution

1. `STOW_BIN` environment variable
2. Package-local `bin/stow` (downloaded from GitHub Release `v{version}`)
3. `bin/stow` relative to the monorepo root (when developing in-repo)
4. `stow` on `PATH`

## Development

```sh
cd packages/stow
npm install
npm run build
npm test
```

Integration tests are skipped when no `stow` binary is available.
