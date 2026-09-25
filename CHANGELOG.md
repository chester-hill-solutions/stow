# Changelog

## Unreleased

- Close the 0.2 remediation gate with race-safe server lifecycle, atomic filesystem object records, backend parity checks, write-ahead durable outbox recovery, ordered per-key propagation, admin metrics/retry controls, and expanded conformance coverage.
- Add a memory-only direct embedded Go runtime with explicit paginated results, bounded/TTL separate run-through caches, and a versioned `js/wasm` host bridge without changing the process/endpoint TypeScript contracts.
- Coordinate file-backed outbox workers across processes with expiring per-entry claims and a filesystem lock.
- Reconcile an attempted outbox entry against upstream before re-propagating it: a retried put whose upstream ETag already matches the immutable local version, or a retried delete whose object is already absent, is acknowledged instead of replayed. The remaining duplicate-delivery window is an upstream that cannot be read during recovery, or an object another writer has since replaced.
- Reject durable outbox files written by a newer schema revision instead of silently dropping claim and reconciliation fields; unversioned files migrate, and attempted legacy entries are marked for reconciliation.

## 0.1.0

- Initial local S3-compatible development service and TypeScript wrapper.
