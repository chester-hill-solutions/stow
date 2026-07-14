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

## Gaps / not yet covered

- **Virtual-hosted URL style** (§2.8): path-style only in this suite; virtual-hosted smoke test pending.
- **ListObjectsV2** delimiter, pagination, `encoding-type=url` (§2.4): prefix-only coverage today.
- **CopyObject** cross-bucket metadata directive / preconditions (§2.6): basic same-bucket copy only.
- **Presigned URL** expiry and OPTIONS preflight (§2.3): happy-path GET/PUT only.
- **Run-through / upstream** (§6.4): gated by `STOW_CONFORMANCE_UPSTREAM=1`; not implemented yet.
- **Node AWS SDK v3** (`@aws-sdk/client-s3`): not in this Go package; add under `packages/stow` if needed.
