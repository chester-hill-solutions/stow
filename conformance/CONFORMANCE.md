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
| Shared corpus + response status | `TestSharedCorpusRoundTrip` (memory/filesystem) |

## Gaps / not yet covered

- **Virtual-hosted URL style** (§2.8): path-style only in this suite; virtual-hosted smoke test pending.
- **ListObjectsV2** delimiter, pagination, `encoding-type=url` (§2.4): prefix and direct-runtime cursor coverage exists, but the shared declarative corpus is still round-trip-focused.
- **CopyObject** cross-bucket metadata directive / preconditions (§2.6): basic same-bucket copy and focused preconditions are covered; the full operation/status matrix is pending.
- **Presigned URL** expiry and OPTIONS preflight (§2.3): happy-path GET/PUT only.
- **Run-through / upstream** (§6.4): one disposable endpoint is gated by `STOW_CONFORMANCE_UPSTREAM=1`; the AWS/R2/custom-provider matrix is still future work.
- **Node AWS SDK v3** (`@aws-sdk/client-s3`): shared corpus and lifecycle coverage live under `packages/stow`; direct-runtime differential cases remain future work.
