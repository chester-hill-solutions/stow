---
status: accepted
---

# SDK compatibility profile and mirror-writes

This ADR extends [ADR 0001: Auto-detect run-through mode](0001-auto-detect-run-through.md). It records the decisions agreed for the 0.2.0 remediation release. Where this ADR conflicts with the earlier ADR or the original compatibility contract, this ADR is normative until the corresponding contract sections are amended and linked here.

## Context

The original implementation followed the documented S3 contract, but it also inherited strict assumptions that are not required for the AWS SDKs used by Stow consumers. At the same time, the run-through path can mutate live provider data, and the original storage backends duplicate behavior. The remediation must make the service predictable for the pinned SDKs without weakening local safety boundaries.

## Decisions

### 1. Compatibility profile

The compatibility target is the observable behavior of version-pinned AWS SDK v3 for Node and AWS SDK for Go v2. Stow supports the safe union of behavior emitted and accepted by both SDKs. It does not attempt to reproduce undocumented Amazon-service strictness or every S3 API outside the shared test profile.

Benign unknown SDK headers are ignored. Headers and query parameters that identify an unsupported semantic operation are rejected before dispatch. Raw HTTP tests cover authentication, routing, safety, malformed input, and protocol edges; they do not require undocumented Amazon behavior.

The tested SDK versions are pinned in the npm lockfile and CI configuration. Dependency upgrades are separate changes that update the profile and corpus together.

### 2. Safety invariants

SDK compatibility does not relax the following boundaries:

- upstream credentials never authenticate the local endpoint, and local credentials never authenticate upstream;
- path escapes and filesystem traversal are rejected;
- invalid, expired, or incorrectly signed requests are rejected;
- live upstream writes require an explicit policy or flag opt-in;
- admin and metrics routes are loopback-only by default;
- credentials and secret values are never written to normal logs or metrics.

### 3. Authentication compatibility

Header authentication may use the SDK-compatible `Date` header when `X-Amz-Date` is absent, provided the selected date header is included in `SignedHeaders` and the signature validates. Presigned URLs continue to require `X-Amz-Date`.

Presigned requests are limited to the supported methods, and `X-Amz-Expires` must be positive and no greater than 604800 seconds. The contract and corpus define the exact boundary and error responses for 604800, 604801, expiry, skew, and malformed credentials.

### 4. Bucket and key names

The local relaxed bucket grammar is 3–63 characters from the ASCII set `[a-z0-9._-]`. Leading and trailing hyphens or underscores are permitted by this local extension. Path separators, control characters, and traversal syntax are never permitted in a bucket name. S3 reserved-name and IP-address rules remain rejected unless the contract is amended again.

Object keys are opaque S3 strings. `.` and `..` may occur in keys. The persistence layer must use a reversible, collision-free encoding so those characters cannot escape a bucket directory. The HTTP layer decodes the request path exactly once.

### 5. Policies and propagation

`local` is a mode, not a policy. The public policies are:

- `readThroughCache`: local storage is authoritative; reads may be populated from upstream; writes stay local unless the separate live-write flag is enabled;
- `mirrorWrites`: read-through behavior plus upstream propagation for supported mutations;
- no public `proxy` policy.

`mirrorWrites` is an explicit opt-in, prints a prominent startup warning, and reports the active write policy. The legacy `proxy` value is rejected with a migration message rather than silently treated as another policy.

A supported local mutation commits locally before upstream propagation. A durable, per-key ordered outbox records an immutable versioned reference to the local result. Transient failures retry with bounded backoff; deterministic failures remain inspectable. The original request returns the upstream failure after the local commit, while the outbox retains the exact result for later propagation.

### 6. Conditional operations and checksums

The shared SDK profile includes atomic `If-None-Match: *` and `If-Match` conditional writes, conditional GET/HEAD validators, and the common checksum set `Content-MD5`, CRC32, CRC32C, SHA-1, and SHA-256. The contract defines header names, encodings, response headers, multipart behavior, and error codes. Unknown checksum algorithms fail clearly.

### 7. Versioning

The existing rule requiring a major version bump is amended for the pre-1.0 line: `0.2.0` may contain documented breaking behavior because it is not yet the stable `@chs/stow` 1.x contract. The 1.x boundary is reserved for the first stable, non-breaking contract. The npm version, binary build version, and status version must come from one version source.

### 8. Backends and storage format

Filesystem storage is the default. Memory storage is an explicitly selected, documented ephemeral backend and must pass the same behavioral storage contract.

The 0.2.0 filesystem format uses atomic records. The old sidecar format is not migrated. Startup emits a warning when an old layout is detected and continues with the new format.

## Consequences

- The contract, glossary, README, package documentation, and conformance corpus must be amended before implementation behavior changes.
- The storage API must expose lifecycle, conditional, checksum, copy, multipart-enumeration, and per-key batch outcomes needed by the shared semantic core.
- The conformance suite must run both SDKs against both backends and include raw safety cases.
- Live-provider tests use disposable resources and short-lived credentials; they are not allowed to target application buckets.
- Upstream session tokens are supported so short-lived provider credentials can be used.
- The release process must not publish a tag until local gates and the recorded live-provider release run pass.

## Out of scope

Production S3 durability, full proxy behavior, Bun support, migration of old data, wildcard DNS/TLS, automatic upstream bucket creation, and unlisted S3 services remain out of scope.
