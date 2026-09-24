# @chs/stow

TypeScript client for the [stow](https://github.com/chester-hill-solutions/stow) local S3-compatible dev bucket service.

## Install

```sh
npm install @chs/stow
```

Build the Go binary first when working from the monorepo:

```sh
make build
```

## Usage

```ts
import { Stow } from "@chs/stow";

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

`Stow.start()` owns the child process it launches. Use `backend: "memory"` for an explicitly ephemeral instance. To use an already-running endpoint, call `Stow.connect({ endpoint, accessKeyId, secretAccessKey, region })`; its connection is disconnected by the caller rather than by the managed-process `stop()` method.

CLI equivalent:

```sh
stow serve --data-dir .dev-bucket --port 9000
```

## Binary resolution

1. `STOW_BIN` environment variable
2. `bin/stow` relative to the monorepo root (when developing in-repo)
3. `stow` on `PATH`

## Development

```sh
cd packages/stow
npm install
npm run build
npm test
```

Integration tests are skipped when no `stow` binary is available.
