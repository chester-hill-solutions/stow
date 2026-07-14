---
status: accepted
---

# Auto-detect run-through mode from environment variables

When `Stow.start()` is called without an explicit mode, stow inspects environment variables for upstream S3-compatible credentials (`STOW_*` > `S3_*` > `AWS_*`). If endpoint, access key, and secret key are present, stow starts in run-through mode with the `readThroughCache` policy. Otherwise it starts local-only. Writes always stay on the local object store unless `allowLiveWrites: true` is set explicitly. Developers can force local-only behavior with `STOW_MODE=local`.

We chose auto-detect over local-default because CHS applications already configure S3 via standard AWS/S3 environment variables. Requiring developers to rename vars or pass explicit upstream config duplicates configuration they already have in `.env` files. The trade-off is surprising behavior when a `.env` copied from staging silently enables upstream reads — mitigated by a loud startup banner, read-only default writes, ETag-based cache revalidation, and the `STOW_MODE=local` override.

## Considered Options

- **Local-default:** Safer mental model ("stow is always local unless I opt in") but forces every run-through user to configure upstream explicitly despite already having live vars in their environment.
- **Full proxy:** Forwards all traffic to upstream for exact live behavior; rejected as default because it hammers staging/production on every read and enables accidental mutations without explicit opt-in.
- **Single-bucket run-through:** Only proxy the bucket named in `S3_BUCKET`; rejected because multi-bucket apps are common and bucket names in SDK requests should map transparently to upstream.

## Consequences

- Startup must print mode, upstream endpoint (credentials redacted), write policy, and override hints on every start.
- ADR and CONTEXT glossary must define Auto-Detect Mode, Read-Through Cache, and Upstream Configuration distinctly from Local-Only Mode.
- Conformance tests must cover both local-only and run-through paths against AWS S3, Cloudflare R2, and custom endpoints.
