# Changelog

## Unreleased

- Close the 0.2 remediation gate with race-safe server lifecycle, atomic filesystem object records, backend parity checks, write-ahead durable outbox recovery, ordered per-key propagation, admin metrics/retry controls, and expanded conformance coverage.
- Add a memory-only direct embedded Go runtime with explicit paginated results, bounded/TTL separate run-through caches, and a versioned `js/wasm` host bridge without changing the process/endpoint TypeScript contracts.
- Coordinate file-backed outbox workers across processes with expiring per-entry claims and a filesystem lock; crash recovery remains at-least-once.

## 0.1.0

- Initial local S3-compatible development service and TypeScript wrapper.
