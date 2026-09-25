# SDK Conformance Tests

Local-mode tests in this package exercise the real `s3api.Server` with **SigV4 auth** (not `DevBypass`). The shared corpus is run through the AWS SDK for Go v2 (`service/s3`) and the Node AWS SDK v3 (`@aws-sdk/client-s3`).

Contract reference: [docs/compat-contract.md](../docs/compat-contract.md) §2 SDK flows.

## Run

```bash
make test-conformance
# or
go test ./conformance/... -count=1 -v
# Node shared-corpus runner (after the package build)
cd packages/stow && npx tsx --test test/integration.test.ts
```

## Shared declarative corpus

`corpus/cases.json` is the single source of truth for cross-SDK behavior. Each
case is isolated, declares its own setup and expected SDK-visible result, and is
executed by both runners:

- the Go runner in `corpus_*.go` uses the AWS SDK for Go v2 and the configured
  conformance store (`STOW_CONFORMANCE_BACKEND`, including the runtime adapter);
- the Node runner in `packages/stow/test/shared-corpus.ts` uses
  `@aws-sdk/client-s3` against a fresh local instance for each case.

The corpus currently covers put/get round-trips (including opaque and empty
keys), conditional reads/writes and checksum writes, paginated `ListObjectsV2`
with prefix, delimiter and `encoding-type=url`, cross-bucket copy with
`REPLACE` metadata and copy preconditions, and a two-part multipart lifecycle
with upload/parts listing. A case may add setup objects, request options, and
expectations (`status`, body, metadata, error code, ETag, checksum, list pages, or part
numbers) without adding a second hand-written SDK suite. Raw HTTP safety and
unsupported-operation cases remain in the protocol tests, not in this corpus.

## Coverage (local mode)

| Flow | Test |
|------|------|
| PutObject round-trip | `TestPutGetRoundtrip` |
| HeadObject metadata | `TestPutGetRoundtrip` |
| ListObjectsV2 prefix | `TestListObjectsV2Prefix` |
| DeleteObject | `TestDeleteObject` |
| CopyObject | `TestCopyObject` |
| Range GET | `TestRangeGetObject` |
| Multipart upload | `TestMultipartUpload` |
| Presigned GET/PUT | `TestPresignedGetPut` |
| DeleteObjects batch | `TestDeleteObjects` |
| SigV4 enforced | `TestSigV4RejectsUnsigned` |
| Shared declarative corpus | `TestSharedCorpus` (Go v2; memory/filesystem/runtime adapter) and the Node shared-corpus suite |

## Live-provider matrix

`.github/workflows/live.yml` is both manually dispatchable and reusable by the
release gate. It always renders these explicit profiles:

| Matrix profile | Active when the configured endpoint host is |
|-----------------|--------------------------------------------|
| `aws-s3` | An Amazon S3 endpoint (`*.amazonaws.com` or `*.amazonaws.com.cn`) |
| `cloudflare-r2-custom` | A Cloudflare R2 endpoint or another non-AWS S3-compatible endpoint |

The matrix deliberately reuses the repository's existing provider-neutral
configuration names: `STOW_LIVE_ENDPOINT`, `STOW_LIVE_ACCESS_KEY_ID`,
`STOW_LIVE_SECRET_ACCESS_KEY`, optional `STOW_LIVE_SESSION_TOKEN`, and the
`STOW_LIVE_BUCKET` / `STOW_LIVE_BUCKET_PREFIX` variables. It does not add or
invent provider-specific credentials. One invocation can authenticate to one
concrete provider, so `provider=auto` selects the matching matrix profile from
the endpoint and explicitly skips the other. To collect AWS and R2/custom
evidence, point the existing configuration at one provider, dispatch and retain
that run, then repeat with the other provider. A successful run is not reported
as coverage for a provider that was skipped.

A live run requires a non-empty disposable prefix, the explicit
`STOW_CONFORMANCE_DISPOSABLE=1` acknowledgement, and a unique key scoped by
provider, GitHub run/attempt, and nonce. The test mirrors one object, verifies
it directly through the upstream client and the adapter, deletes it, then
requires both `HeadObject` and a prefix listing to confirm that the key is gone.
Cleanup failures fail the test rather than being logged and ignored.

Missing configuration is an explicit skip for scheduled/manual development
runs. The reusable release call sets `require_configured=true`, so a tag cannot
publish without a configured live run. Select a concrete profile with a manual
dispatch, or validate the matrix wiring without network access:

```bash
STOW_LIVE_PROFILE=aws-s3 STOW_CONFORMANCE_DRY_RUN=true \
  ./conformance/live-provider.sh resolve
STOW_CONFORMANCE_DRY_RUN=true ./conformance/live-provider.sh test
```

The same dry-run path compiles and invokes the live test, which skips before
configuration or network access. With no opt-in variables, a normal
`go test ./conformance/...` also skips the live test.

## Gaps / not yet covered

- **Virtual-hosted URL style** (§2.8): path-style only in this suite; virtual-hosted smoke test pending.
- **ListObjectsV2** token bounds/invalid-token behavior (§2.4): the shared corpus covers prefix, delimiter, `encoding-type=url`, and multi-page continuation; the negative token matrix is pending.
- **CopyObject** remaining conditional/status combinations (§2.6): cross-bucket copy, `REPLACE` metadata, and `if-none-match` are in the shared corpus; the full date/if-match matrix is pending.
- **Presigned URL** expiry and OPTIONS preflight (§2.3): happy-path GET/PUT only.
- **Run-through / upstream** (§6.4): the provider matrix and disposable object lifecycle are implemented, but the live suite still covers one mirror round-trip rather than all six contract scenarios.
- **Node AWS SDK v3** (`@aws-sdk/client-s3`): the shared corpus runs through the Node runner in `packages/stow/test/shared-corpus.ts`; direct-runtime differential cases remain future work.
