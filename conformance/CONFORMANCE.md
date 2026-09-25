# SDK Conformance Tests

Local-mode tests in this package exercise the real `s3api.Server` with **SigV4 auth** (not `DevBypass`) using the AWS SDK for Go v2 (`service/s3`).

Contract reference: [docs/compat-contract.md](../docs/compat-contract.md) §2 SDK flows.

## Run

```bash
make test-conformance
# or
go test ./conformance/... -count=1 -v
```

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
| Shared corpus + response status | `TestSharedCorpusRoundTrip` (memory/filesystem/runtime adapter) |

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
- **ListObjectsV2** delimiter, pagination, `encoding-type=url` (§2.4): prefix and direct-runtime cursor coverage exists, but the shared declarative corpus is still round-trip-focused.
- **CopyObject** cross-bucket metadata directive / preconditions (§2.6): basic same-bucket copy and focused preconditions are covered; the full operation/status matrix is pending.
- **Presigned URL** expiry and OPTIONS preflight (§2.3): happy-path GET/PUT only.
- **Run-through / upstream** (§6.4): the provider matrix and disposable object lifecycle are implemented, but the live suite still covers one mirror round-trip rather than all six contract scenarios.
- **Node AWS SDK v3** (`@aws-sdk/client-s3`): shared corpus and lifecycle coverage live under `packages/stow`; direct-runtime differential cases remain future work.
