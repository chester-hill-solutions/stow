# Stow 0.2.0 Remediation Execution Plan

**Status:** agreed execution plan
**Baseline:** `main` at `63c8a3a`
**Release target:** `0.2.0`
**Primary compatibility target:** observable behavior of pinned AWS SDK v3 (Node) and AWS SDK for Go v2
**Safety boundary:** SDK compatibility never relaxes local credential, path-safety, authentication, or explicit live-write protections

## 1. Outcome and release gate

Ship a Docker-free local S3-compatible service whose externally observable behavior passes a shared, version-pinned SDK conformance suite. The release must include:

- a shared semantic storage core with behavioral parity between filesystem and memory backends;
- the local and run-through modes defined by the amended compatibility contract;
- safe mirror-write propagation with a durable, retryable outbox;
- the complete supported S3 operation set, including multipart listing and SDK-observable conditional/checksum behavior;
- the TypeScript `@chester-hill-solutions/stow-s3` wrapper with explicit backend selection and a separately typed `connect()` lifecycle;
- accurate admin status/inspection and Prometheus metrics;
- a full local CI gate, protected live-provider tests, binary artifacts, and npm artifacts.

No `0.2.0` tag or artifact publication is allowed until every exit criterion in Phase 11 passes.

## 2. Locked decisions and required contract amendments

The existing `docs/compat-contract.md` remains the acceptance document, but the following decisions must be recorded before implementation:

1. **SDK profile:** pin the tested Node `@aws-sdk/client-s3` and Go `service/s3` versions in the repository lockfiles and CI. Support the safe union of behavior emitted/accepted by both SDKs. Do not require undocumented Amazon-only strictness.
2. **Safety invariants:** retain local credential separation, path-escape protection, SigV4/expiry validation, loopback-only admin/metrics defaults, explicit live-write opt-in, and secret-safe logging.
3. **SDK-driven semantics:** ignore benign unknown headers, reject semantic markers for unsupported operations, use an SDK-compatible subset of S3 error codes, and support SDK-exposed conditional writes/checksums where safe.
4. **Date fallback:** accept the SDK-compatible `Date` fallback where signed authentication remains valid; amend the contract to document this deviation from strict `X-Amz-Date` requirements.
5. **Naming:** use the agreed minimal S3 relaxation (S3 length/character baseline, with permitted edge hyphens/underscores), while retaining path safety and reserved-name rules. Preserve `..` as valid object-key content using safe internal mapping.
6. **Backends:** make filesystem the default and expose an explicit `filesystem | memory` backend option. Memory is a supported ephemeral backend, not merely a test double.
7. **Policies:** public policies are `local`, `readThroughCache`, and `mirrorWrites`; remove or hide the old `proxy` policy. `mirrorWrites` means read-through plus upstream propagation and must produce a loud startup warning.
8. **Write durability:** local commit precedes upstream propagation; failed propagation creates a per-key ordered outbox entry with an immutable local reference. Retry transient failures automatically and expose loopback-only manual retry/discard controls.
9. **Package lifecycle:** Node 20+ is supported. `Stow.start()` owns a spawned process; `Stow.connect()` returns a distinct external-connection type and requires explicit endpoint/credential inputs. Remove the reserved unused `upstream` start option.
10. **Storage format:** 0.2.0 uses a clean-break atomic record format. Old data is not migrated; startup emits a warning and continues with the new format.
11. **Operations:** the amended contract includes ListMultipartUploads, full conditional CopyObject, single-range support, SDK-supported conditional writes, and the common checksum set (`Content-MD5`, CRC32, CRC32C, SHA-1, SHA-256).
12. **Release identity:** `0.2.0` is the first release targeting the amended v1 contract. Publish both the Go binary and `@chester-hill-solutions/stow-s3` after the full gate passes.

Update `docs/adr/0001-auto-detect-run-through.md` through a new amendment ADR rather than rewriting accepted history. Update `docs/compat-contract.md`, `CONTEXT.md`, `README.md`, `packages/stow-s3/README.md`, and a new changelog/release-note document.

### Normative contract tables required in R0

R0 is not complete until the amended documents contain explicit tables for the following; implementation must not infer these rules from current handler code:

- **Versioning:** amend the existing “major bump for §1/§6 changes” rule. `0.2.0` is a pre-1.0 release with documented breaking behavior; the stable `@chester-hill-solutions/stow-s3` 1.x boundary is reserved for the first non-breaking stable contract. Add one version source for npm, the binary, and `/_stow/status`.
- **Policies:** `local` is a mode, not a policy. `readThroughCache` remains local-only unless the separate live-write flag is set; `mirrorWrites` explicitly enables propagation and emits a warning; `proxy` is rejected/deprecated with a migration error, not silently accepted.
- **Bucket grammar:** specify the exact accepted alphabet, length, reserved-name/IP rules, and permitted edge characters in the ADR and contract. Use one validator shared by storage and HTTP.
- **Date fallback:** apply the fallback only to header authentication; the selected date header must be part of `SignedHeaders`; presigned URLs continue to require `X-Amz-Date`.
- **Auth/error behavior:** define required signed headers, allowed presigned methods, expiry boundary (604800/604801), expiry, skew, checksum/conditional errors, request resource paths, and request-ID/CORS requirements.
- **Conditional/checksum wire format:** list header names, encodings, supported operations, response headers, and error codes for `If-Match`, `If-None-Match`, conditional GET/HEAD, Content-MD5, CRC32, CRC32C, SHA-1, and SHA-256.
- **Listing/multipart:** define continuation-token versus `start-after` precedence, invalid/zero/out-of-range `max-keys`, token format/invalidation, all URL-encoded response fields, upload-ID scope, ListParts pagination, and ListMultipartUploads fields.
- **Batch delete:** define the storage result model, per-key errors, quiet mode, partial-success behavior, and request-size limits.

The exact SDK versions, corpus schema, and these tables are the source of truth for R1 and all later acceptance tests.

## 3. Non-goals

- Production S3 parity or production durability guarantees.
- Full proxy mode.
- Docker, TLS termination, wildcard DNS, or automatic bucket creation upstream.
- Versioning, Object Lock, ACL enforcement, policies, IAM/STS, KMS/SSE, lifecycle, replication, notifications, S3 Select, batch operations, or tagging APIs.
- Bun as a supported runtime.
- Migration or preservation of the old `.stow`/sidecar format.
- Tracking every future SDK release or every S3 service outside the pinned common subset.

## 4. Dependency graph and delivery shape

Use small, independently reviewable pull requests. The critical path is:

`R0 contract → R1 characterization → R2 shared core → R3 adapters → R5/R6 operations → R7 run-through/outbox → R8 package → R10 CI/release → R11 final gate`

`R4 auth/routing/errors` can proceed in parallel with R2/R3 after R0, but all operation work depends on its interfaces. `R9 observability/security` can be implemented alongside R7 and must be integrated before R10.

Every phase must leave the branch green. Known current gaps are recorded as expected-failure cases until their owning phase turns them green; do not weaken the release gate to accommodate them.

### R-Standards — Ratcheted code-quality gates (parallel, required)

The repository adopts the CallCaster/GoCanvass ratchet pattern before further feature work:

- `docs/CODE_STANDARDS.md` defines the policy and commands.
- `tools/quality` scans Go function length, complexity, parameter count, `any`, and `panic` identities against `scripts/baselines/go-quality.json`.
- `packages/stow-s3/eslint.config.mjs` ratchets TypeScript complexity, depth, parameter count, function length, console usage, type escapes, and duplicate imports.
- `scripts/check-lint-ratchet.mjs`, `scripts/check-type-escapes.mjs`, `scripts/check-dry.mjs`, `scripts/check-file-size.mjs`, and `scripts/check-coverage.mjs` compare current results with checked-in baselines.
- `make standards` is the local gate; CI and release verification must run it.
- Baselines may only shrink after verified debt reduction. A baseline update is a reviewed maintenance action, never an approval mechanism for a regression.
- The nested `packages/stow-s3/go.mod` boundary prevents Go package discovery from walking npm `node_modules`.

This standards phase is a release blocker: no new remediation PR may increase a debt baseline or lower the coverage floor without an explicitly reviewed, verified change.

## 5. Work packages

### R0 — Contract and architecture decisions (blocking)

**Purpose:** remove contradictions before changing behavior.

**Touchpoints:**

- `docs/compat-contract.md`
- `docs/adr/0001-auto-detect-run-through.md`
- new ADR amendment under `docs/adr/`
- `CONTEXT.md`
- `README.md`
- `packages/stow-s3/README.md`
- new changelog/release notes

**Work:**

1. Add the SDK compatibility profile, exact tested versions, safe-union rule, and raw-HTTP test boundary.
2. Amend bucket naming, date-header handling, conditional writes/checksums, memory backend, public policies, `mirrorWrites`, outbox behavior, admin/metrics routes, and `Stow.connect()`.
3. Define the language-neutral conformance corpus schema and scenario IDs.
4. Record the clean-break storage format and 0.2.0 version boundary.
5. Add a traceability table from every contract section/requirement to a conformance scenario or explicit non-goal.

**Exit criteria:** no unresolved contradiction between ADR, compatibility contract, README, glossary, and package API docs; reviewers can trace every new public behavior to a test scenario.

### R1 — Baseline characterization and test infrastructure (blocking)

**Purpose:** make the foundation refactor safe and establish the shared test source of truth.

**Touchpoints:**

- `Makefile`
- `conformance/`
- `packages/stow-s3/test/`
- `packages/stow-s3/package.json`
- `.gitignore` (stop ignoring the committed npm lockfile)
- new `conformance/corpus/` or equivalent language-neutral scenario directory
- `go.mod`, `go.sum`, npm lockfile

**Work:**

1. Run and record the current `go test ./...`, `make test-conformance`, `go vet ./...`, `npm ci`, `npm run build`, and `npm test` results before behavior changes. Add a reproducible aggregate conformance target rather than relying on `make test-conformance` alone.
2. Convert the existing Go SDK coverage into named scenarios and add expected-failure entries for the review findings rather than silently skipping them. Run the corpus against both filesystem and memory backends, not only the current memory harness.
3. Add a declarative corpus containing setup, operation, request expectations, status/code/header assertions, and semantic XML assertions. Keep raw HTTP cases for safety/protocol edges. Include both SDKs, both backends, path/virtual-host routing, auth/presign boundaries, unsupported markers, conditional/checksum headers, opaque keys, multipart lifecycle, and run-through cases.
4. Implement thin Go and Node runners over the same corpus. Do not maintain two unrelated hand-written suites. Make the Node runner compatible with Node 20 (the current `--experimental-strip-types` script cannot be the Node 20 test mechanism).
5. Add a deterministic mock S3 upstream with fake time, call counters, configurable ETag changes, 404s, throttling, transient failures, and permission failures. Treat missing or permanently skipped scenarios as CI failures for required cases.
6. Pin SDK versions with a committed npm lockfile and explicit CI versions; keep dependency upgrades in separate changes. Ensure the binary used by wrapper integration tests is built at the resolver’s expected path, and do not allow the integration test to silently skip in CI.
7. Add the Node 20+ CI matrix and ensure the local Go and Node runners use the same generated credentials, endpoint fixtures, and cleanup fixtures.

**Exit criteria:** every planned contract behavior has a scenario ID; the current implementation’s known gaps are visible and reproducible; the test harness itself is green.

### R2 — Shared storage semantic core (blocking)

**Purpose:** remove semantic drift between `FilesystemStore` and `MemoryStore` before restoring S3 behavior.

**Touchpoints:**

- `internal/storage/store.go`
- `internal/storage/types.go`
- `internal/storage/util.go`
- new domain/core files under `internal/storage/`
- storage contract tests

**Work:**

1. Define a narrow persistence boundary rather than letting each backend implement all S3 semantics. Keep the pure core independent of `net/http` and AWS SDK types. Freeze the final `Store`/service API before adapter work: it must include conditional write options, checksum options, copy metadata/precondition options, per-key batch-delete outcomes, multipart enumeration, and lifecycle/close hooks.
2. Introduce explicit domain values for object locations, copy requests, list entries, upload state, part records, checksums, and write intent. Include stable record identities/version identifiers so an outbox can reference the exact committed object version.
3. Move shared behavior into the core:
   - bucket/key validation and canonical metadata;
   - UTF-8 byte ordering and merged list pagination;
   - ETag and checksum calculation;
   - atomic conditional read/write decisions;
   - copy metadata directives and preconditions;
   - multipart state transitions, part validation, and composite ETag calculation;
   - batch-delete outcome and ordering semantics.
4. Keep v1 buffering behavior explicit for single-part and multipart completion; do not introduce streaming storage as a side effect.
5. Make `MaxKeys` count objects and common prefixes in one merged ordered result. Continuation tokens must round-trip, remain stable, and follow the normative precedence/bounds rules from R0.
6. Define backend capabilities and constructor boundaries so filesystem and memory implementations are interchangeable through the same store contract. Make `Close`/shutdown explicit so the outbox and persistence layers can be drained safely.
7. Add a backend contract suite that can run against every implementation and reports the first semantic divergence.

**Exit criteria:** the core owns all cross-backend semantics; the old duplicate implementations are either adapters over the core or explicitly justified deviations; the contract suite is shared by both backends.

### R3 — Persistence adapters and atomic records (blocking)

**Purpose:** make the new foundation durable, recoverable, and safe for opaque S3 keys.

**Touchpoints:**

- `internal/storage/fs.go`
- `internal/storage/fs_atomic.go`
- `internal/storage/fs_list.go`
- `internal/storage/fs_multipart.go`
- `internal/storage/memory.go`
- new storage factory/configuration files

**Work:**

1. Replace split object bytes plus `.stowmeta` records with one atomically replaced record containing bytes and metadata. Use an invisible staging namespace, parent-directory synchronization where supported, and atomic rename.
2. Add a format marker, explicit `Open`/`Close` lifecycle, and a single-owner data-directory lock. A second process must fail clearly and locks must be released on every shutdown/error path.
3. Implement best-effort legacy-layout detection solely to emit the agreed clean-break warning; never migrate or partially reuse old data. Define deterministic recovery for active multipart and outbox state rather than blindly deleting `.multipart`.
4. Replace the current `path.Clean`-based key mapping with a reversible encoded-key namespace so `.`, `..`, repeated slashes, and reserved suffixes are valid object content without escaping the bucket. Decode keys exactly once at the HTTP boundary. Add tests for `%2F`, `%252F`, `..`, repeated slashes, and reserved suffixes.
5. Implement startup recovery: discard incomplete staging records, retain committed records, reconcile unresolved write intents/outbox entries, and never expose staging files in listings.
6. Add fine-grained per-bucket/object coordination and a consistent listing snapshot without relying on one global lock.
7. Expose an explicit backend factory for `filesystem` and `memory`; filesystem remains the default. Memory must pass the same store contract suite and clearly document non-durability; a durable outbox must not be silently backed by ephemeral memory.
8. Ensure active multipart uploads count as bucket content for deletion purposes and are recovered or explicitly discarded according to the lifecycle contract.

**Exit criteria:** filesystem and memory pass the shared contract suite; crash/recovery tests pass; no object-key input can escape the data directory; old-format startup follows the agreed warn-and-continue behavior.

### R4 — Authentication, routing, errors, and server boundary (parallel after R0)

**Purpose:** establish one safe protocol boundary before operation handlers grow.

**Touchpoints:**

- `internal/auth/auth.go`
- `internal/auth/sigv4.go`
- `internal/s3api/auth.go`
- `internal/s3api/router.go`
- `internal/s3api/request.go`
- `internal/s3api/errors.go`
- `internal/s3api/server.go`
- `internal/s3api/cors.go`
- new `internal/s3api/dispatch.go` (move S3 dispatch out of `admin.go`)
- `cmd/stow-s3/main.go` and `packages/stow-s3/src/start.ts` (base-host/exposure plumbing)

**Work:**

1. Make production authentication fail closed; `s3api.New` must reject a missing production auth configuration rather than substituting `DevBypass`. `DevBypass` remains an explicit test-only constructor option.
2. Implement the SDK profile: signed host/date requirements, the documented header-auth `Date` fallback (with the selected date header signed), region/scope checks, payload handling, skew, presign methods/expiry boundaries, and malformed-signature failures. Include 604800/604801, expired URL, unsupported method, and unsigned cases in the corpus.
3. Centralize S3 error mapping with stable request IDs, resource paths, and CORS headers. Define the exhaustive status/code/resource table for auth, routing, range, checksum, conditional, and multipart failures; use exact codes for known SDK-observable errors and a stable fallback for unknown failures.
4. Replace path parsing with one route representation shared by auth, dispatch, and error paths: split on literal `/`, decode each segment once, preserve encoded slashes inside keys, and reject encoded separators/traversal-like bucket segments. Cover malformed escapes and double-encoded input in raw tests.
5. Add explicit `STOW_BASE_HOST`/configuration for virtual-hosted-style Host routing and validate it before the listener starts. Keep path-style as the local default and pass the same base-host setting through CLI and TypeScript start options.
6. Build an operation-by-operation semantic-marker matrix (versioning, ACL, policies, tagging, encryption, `versionId`, and other unsupported APIs) and reject markers before dispatch. Ignore only benign unknown SDK headers.
7. Move S3 dispatch out of `admin.go` into a dedicated dispatcher file so admin observability and protocol routing have separate ownership.
8. Add structured request logging with request IDs, operation/status/latency, redacted identifiers, and no secrets.

**Exit criteria:** auth, routing, CORS, error, and log behavior pass SDK and raw safety tests; no public bind exposes admin/metrics by accident.

### R5 — Bucket and single-part object operations (depends on R2–R4)

**Purpose:** close the core S3 compatibility gaps found in the review.

**Touchpoints:**

- `internal/s3api/handlers_bucket.go`
- `internal/s3api/xml.go`
- `internal/s3api/errors.go`
- `internal/storage` core and adapters
- conformance corpus

**Work:**

1. Implement the amended bucket-name grammar consistently in the shared core and HTTP boundary.
2. Make CreateBucket idempotent, HeadBucket/DeleteBucket accurate, and DeleteBucket reject objects or active multipart uploads.
3. Enforce Content-Length against the original declared value (preserve it across SigV4 body buffering), canonical metadata, 2 KiB metadata limits, default content type, and key limits in one place.
4. Implement SDK-supported conditional writes atomically: `If-None-Match: *` and `If-Match`, with 412 on mismatch. Also implement conditional GET/HEAD validators required by the amended contract, including quoted/weak ETag and date comparison rules.
5. Validate the common checksum set using the R0 wire table: required header names/encodings, supported operations, response headers, multipart-part behavior, and stable errors. Reject unknown checksum algorithms clearly.
6. Implement full CopyObject source parsing, COPY/REPLACE metadata behavior, and all four conditional copy headers.
7. Make batch delete return per-key outcomes, including idempotently absent keys, without losing earlier successes; preserve quiet-mode semantics and request-size limits.
8. Map all known errors through the central mapper and assert required request IDs/CORS headers.

**Exit criteria:** the operation scenarios pass through both SDK runners and the raw HTTP runner; no backend has a separate validation rule.

### R6 — Listing, ranges, and multipart lifecycle (depends on R2–R5)

**Purpose:** complete the paginated and multipart portions of the SDK profile.

**Touchpoints:**

- `internal/storage/util.go`
- `internal/storage/fs_list.go`
- `internal/storage/fs_multipart.go`
- `internal/storage/memory.go`
- `internal/s3api/handlers_bucket.go`
- `internal/s3api/handlers_multipart.go`
- `internal/s3api/xml.go`
- `internal/s3api/dispatch.go`
- conformance corpus

**Work:**

1. Build one sorted logical list of objects and common prefixes, apply prefix/delimiter grouping, and paginate exactly once. Reuse the core logical-list primitive for run-through merges rather than implementing a second paginator.
2. Apply `MaxKeys` to the combined result; calculate `KeyCount`; apply the R0 precedence/bounds rules for continuation tokens, `start-after`, zero, invalid, and oversized values; URL-encode `Prefix`, `Contents.Key`, `CommonPrefixes.Prefix`, and any delimiter/token fields required by the contract.
3. Support bounded, open-ended, and suffix single ranges; reject multi-range requests with the stable SDK-compatible error.
4. Add `ListMultipartUploads` to the store API and implement deterministic ordering, initiation metadata, `key-marker`/`upload-id-marker`, `max-uploads`, and XML response types. Add ListParts pagination markers and preserve initiation metadata through completion.
5. Validate completion part numbers, ordering, submitted ETags, duplicate/unsorted parts, and the 5 MiB non-final-part rule in the shared core.
6. Compute the AWS composite ETag from concatenated binary part MD5 values plus `-{partCount}`; reject wrong submitted ETags with `InvalidPart`.
7. Scope every upload ID to its initiating bucket/key; mismatched routes return `NoSuchUpload`. Ensure incomplete/aborted uploads never appear as objects and active uploads prevent bucket deletion.
8. Cover abort, list-parts, list-uploads, complete, wrong-part, undersized-part, upload-ID scope, metadata, pagination, and ghost-object scenarios in both SDK suites.

**Exit criteria:** ListObjectsV2 and multipart lifecycle pass the shared corpus for memory and filesystem, including pagination and negative cases.

### R7 — Run-through cache and outbox (depends on R5–R6)

**Purpose:** make run-through behavior explicit, safe, observable, and recoverable.

**Touchpoints:**

- `internal/runthrough/config.go`
- `internal/runthrough/adapter.go`
- `internal/runthrough/upstream.go`
- `internal/s3api/errors.go` (shared upstream-error mapping)
- new outbox/cache modules
- `cmd/stow-s3/main.go`
- run-through conformance scenarios

**Work:**

1. Replace the public `proxy` policy with the three agreed policies. Use one error-returning configuration resolver before opening sockets; reject unknown/contradictory values instead of silently defaulting. Remove proxy-specific upstream-first branches.
2. Preserve `STOW_* > S3_* > AWS_*` precedence and `STOW_MODE=local`; require complete upstream credentials, session-token support for short-lived providers, and an explicit/default-compatible region. Sanitize endpoints through one shared host-only redactor used by startup and status.
3. Make `mirrorWrites` an explicit policy opt-in, print a prominent warning, and report the active write policy in status/startup output. Keep `readThroughCache` local-only unless the separate live-write opt-in is supplied.
4. Use separate authoritative/local and cache stores with explicit provenance (including pending outbox state) so upstream-derived entries cannot be confused with local writes. Do not point the authoritative store at the cache directory.
5. Revalidate cache hits using upstream ETag/Last-Modified; serve stale data on transient upstream failure; evict only upstream-derived objects on upstream 404. Never use a changed ETag as a general ordering signal.
6. Extend the upstream client interface for every operation the amended contract says can propagate, or explicitly mark an operation local-only in the contract. Merge local/upstream listings with local metadata winning, using the shared logical-list primitive.
7. Implement the durable per-key ordered outbox:
   - atomically commit the local record and write intent, or define a reliable reconciliation protocol that cannot lose an intent after a crash;
   - local commit first;
   - immutable, versioned reference to the committed object, not a duplicate payload;
   - preserve operation-specific intent for put, delete, copy, and multipart completion;
   - transient-only automatic retry with bounded backoff;
   - deterministic 4xx failures retained for inspection;
   - reference-counted cleanup after success or explicit discard;
   - loopback-only list/retry/discard actions;
   - the original request returns the upstream failure after local commit, while the durable outbox preserves the exact version for later propagation.
8. Define typed upstream errors with `errors.Is`-compatible classification for missing bucket/key, transient retryable failures, permission/auth failures, and throttling. Map them through the shared S3 error mapper, preserving useful provider status for SDK-observable cases and using stable 502/503 responses for unknown failures.
9. Add deterministic mock tests for every run-through scenario, then opt-in disposable live tests for AWS S3, R2, and a custom endpoint. Require a disposable bucket/prefix guard and cleanup verification.

**Exit criteria:** no local write is lost or silently propagated under policy; retries and failures are observable; mock and live test matrices pass without using application buckets.

### R8 — Observability and security integration (parallel with R7, blocks R10)

**Purpose:** satisfy the amended admin/metrics contract without creating public data leaks.

**Touchpoints:**

- `internal/storage/store.go` and storage stats/telemetry interfaces
- `internal/runthrough/adapter.go` and outbox state
- `internal/s3api/admin.go`
- `internal/s3api/server.go`
- `cmd/stow-s3/main.go`
- `packages/stow-s3/src/start.ts` (public exposure/readiness plumbing)
- new metrics/outbox instrumentation
- `docs/compat-contract.md`

**Work:**

1. Make `/_stow/status` accurate: mode, sanitized upstream host only, region, bucket/object counts, cache/write policy, uptime, version from the single version source, and active outbox state.
2. Make `/_stow/inspect` accurate: bucket snapshots, object counts, multipart uploads, cache hit/miss counters, last upstream error, and outbox entries. Add storage/statistics interfaces rather than hardcoding zeros or counting only the first page.
3. Add `/_stow/metrics` with a documented Prometheus exposition schema for cache behavior, upstream latency/errors, multipart state, retries, and outbox depth. Keep label cardinality bounded and free of bucket/key labels.
4. Keep admin and metrics loopback-only by default; require an explicit public exposure flag and warning if that flag is used. Enforce the decision from the actual listener/remote address, not only the configured bind string.
5. Ensure metrics and logs never contain credentials, raw secrets, or unredacted object keys by default. Keep `STOW_READY` as the explicit credential handoff on a dedicated stdout channel, but never include it in normal logs or startup error dumps; avoid passing secrets as unprotected process arguments.
6. Implement graceful shutdown: stop accepting requests, drain with a bounded timeout, stop outbox work, and close stores cleanly. Align the TypeScript child-process timeout with the Go server’s shutdown budget.
7. Add tests for route exposure, redaction, status accuracy, metric names, readiness-channel handling, and shutdown behavior.

**Exit criteria:** contract-required observability is real rather than hardcoded, and security tests prove public binds do not expose admin/metrics by default.

### R9 — TypeScript package and lifecycle (depends on R7–R8)

**Purpose:** make `@chester-hill-solutions/stow-s3` reflect the new backend and lifecycle contracts without speculative API.

**Touchpoints:**

- `packages/stow-s3/src/types.ts`
- `packages/stow-s3/src/start.ts`
- `packages/stow-s3/src/instance.ts`
- `packages/stow-s3/src/index.ts`
- `packages/stow-s3/src/bin.ts`
- `packages/stow-s3/src/s3-client.ts`
- `packages/stow-s3/test/`
- `cmd/stow-s3/main.go` (backend/public-exposure flags)
- `packages/stow-s3/package.json`, lockfile, `tsconfig.json`
- generated `packages/stow-s3/dist/`

**Work:**

1. Add explicit `backend?: "filesystem" | "memory"` to start options and pass it to the CLI. Define memory-data-directory behavior and reject contradictory options clearly.
2. Keep `Stow.start()` as the managed child-process API. Make startup failure cleanup and `stop()` idempotent, with bounded graceful termination. Keep `Stow.connect()` distinct with explicit endpoint, region, credential/provider, ownership, and close/disconnect semantics.
3. Remove the reserved unused `StartOptions.upstream` field. Retain only environment helpers that are actually consumed and documented.
4. Extract the duplicated ListObjectsV2 continuation loop into one page iterator used by `emptyBucket()` and `snapshotObjects()`.
5. Ensure every created SDK client is destroyed on success and failure; preserve the unsigned-payload middleware required by the local SigV4 verifier.
6. Add wrapper tests for backend selection, process startup failure, repeated stop, explicit connect, credentials, generated types, public exposure, and Node 20+ compatibility. Use a Node-20-compatible TypeScript test mechanism and do not let the integration test silently skip when CI has built the binary.
7. Treat `src` as the source of truth; clean and regenerate `dist` in CI, fail if checked-in output differs, and publish only the declared package files. Document whether the npm wrapper requires `STOW_BIN`/PATH or a separately installed platform binary; do not imply that a bare npm install embeds a binary unless it does.

**Exit criteria:** package builds cleanly on the supported Node matrix, public types match the amended contract, and no reserved/speculative option remains.

### R10 — CI, live tests, and release automation (depends on all implementation phases)

**Purpose:** make regressions and release drift mechanically detectable.

**Touchpoints:**

- `Makefile`
- `.github/workflows/ci.yml`
- `.github/workflows/release.yml`
- `.gitignore` (allow the lockfile)
- `packages/stow-s3/package-lock.json`
- release documentation

**Work:**

1. Add a full local required gate: Go build/vet/tests, race tests where practical, Node build/tests, dual-SDK corpus, raw HTTP safety tests, mock run-through tests, filesystem/memory backend matrices, and generated-output verification. `make test-conformance` must either become this aggregate or be accompanied by one documented aggregate target.
2. Add a Node 20-compatible test runner, Node 20+ matrix jobs, and exact SDK pins. Commit the npm lockfile (remove the current ignore rule), and use a CI-installed test tool or compiled tests rather than Node’s newer `--experimental-strip-types` flag on Node 20.
3. Add protected live jobs for AWS S3, R2, and a custom endpoint. Require disposable buckets/prefixes, short-lived credentials/session tokens, cleanup checks, and no application credentials. Make a successful live run for the release commit a release prerequisite even when the job is scheduled/manual rather than run on every PR.
4. Add branch-protection required checks and make release jobs depend on the complete local gate and the recorded live result. Pin third-party GitHub Actions by commit SHA and use `go-version-file` rather than duplicating version literals.
5. Update the release workflow to verify tag/package/version consistency, build/test/package the Go binary, publish `@chester-hill-solutions/stow-s3` through trusted npm/OIDC publishing from the same tag, generate checksums, verify package contents, and refuse publication on any failed gate.
6. Ensure CI builds the binary at the path the TypeScript resolver expects and fails the Node integration job if its required binary scenario is skipped.
7. Update the changelog and release notes with the clean-break format, SDK profile, policy changes, and migration warning.

**Exit criteria:** a clean checkout reproduces the release candidate; a deliberately broken contract scenario fails CI; no release job can publish an untested tag.

### R11 — Final conformance and release verification (blocking)

**Purpose:** close the loop against the original review and the amended contract.

**Work:**

1. Run the traceability matrix and mark every scenario as passing, explicitly out of scope, or blocked by a documented provider limitation.
2. Verify the original review findings:
   - no repeated write-action switch without a shared executor;
   - no duplicated continuation loop;
   - no unused speculative fields;
   - no middle-man `copyLimited`;
   - one shared metadata conversion path;
   - one copy/location abstraction;
   - one bucket validator;
   - all contract gaps from the review are closed.
3. Run:
   - `gofmt`/`go vet ./...` and `go test ./...`;
   - `go test -race ./...` for local packages;
   - the aggregate local conformance target across both backends and both SDK runners;
   - `make test-conformance` plus the Node 20-compatible package build/test on the supported Node matrix;
   - raw HTTP safety/protocol tests;
   - mock run-through/outbox tests;
   - the required disposable live-provider release run (scheduled/manual execution is acceptable, but the release commit must have a successful recorded result);
   - generated `dist` diff, package-content, and tag/version checks.
4. Review logs/status/metrics manually for secret and key redaction.
5. Publish `v0.2.0` only after the release checklist is signed off.

## 6. Acceptance criteria

### Contract and SDK behavior

- [ ] Pinned Node and Go SDK suites pass the shared corpus.
- [ ] SDK-safe union behavior is documented for divergences.
- [ ] Raw HTTP tests cover security and protocol edges without requiring undocumented Amazon quirks.
- [ ] All supported operations, errors, headers, pagination, ranges, multipart, checksum, and conditional behaviors pass.
- [ ] R0’s normative auth, naming, listing, multipart, checksum, error, and versioning tables are implemented and tested.
- [ ] Conditional GET/HEAD and SDK-supported conditional writes/checksums follow the documented wire contract.
- [ ] Unsupported semantic markers fail predictably; benign SDK headers do not break requests.
- [ ] Local credentials never authenticate upstream and upstream credentials never authenticate locally.

### Storage

- [ ] Filesystem and memory backends pass one behavioral contract suite.
- [ ] Atomic records prevent partial object/metadata visibility.
- [ ] Safe object-key mapping prevents traversal, including `..` content, and handles encoded/repeated separators.
- [ ] Store APIs expose lifecycle/close, multipart enumeration, conditional/checksum, copy, and per-key batch outcomes needed by the handlers.
- [ ] Single-owner locking, crash recovery, staging cleanup, and active-multipart recovery work.
- [ ] Multipart state transitions and composite ETags are correct.
- [ ] Clean-break warning and 0.2.0 documentation are present.

### Run-through

- [ ] Public policies are local, read-through, and mirror-writes only.
- [ ] Live writes require the agreed explicit opt-in and emit a loud warning.
- [ ] Cache provenance, stale-on-transient-error, and protected local-write eviction work.
- [ ] Outbox ordering, immutable references, retries, manual actions, and cleanup work.
- [ ] Mock and disposable live tests pass for the required provider matrix.

### Operations and release

- [ ] Admin and metrics routes are accurate and loopback-only by default.
- [ ] Logs and metrics redact secrets and keys.
- [ ] Graceful shutdown drains requests and stops workers/stores cleanly.
- [ ] Node 20+ build/test and generated `dist` verification pass.
- [ ] Binary and npm artifacts are published from the same verified tag.

## 7. Risks and mitigations

| Risk | Mitigation |
|---|---|
| SDK compatibility expands without bound | Pin versions, define a common subset, and require an explicit contract amendment for new features. |
| Pre-1.0 breaking changes conflict with the old versioning rule | Amend the rule and version source in R0 before implementation; reserve stable 1.x semantics for the post-0.2 contract. |
| Filesystem and memory semantics diverge | One shared semantic core plus one backend contract suite; no backend-specific behavior in handlers. |
| Clean-break format surprises users | Prominent startup warning, 0.2.0 release notes, and no partial legacy reads. |
| Mirror writes affect real data | Loud warning, explicit policy, isolated test buckets, immutable outbox references, and protected retry/discard controls. |
| Outbox grows or retries stale operations | Per-key ordering, reference-counted retention, transient-only retry, bounded backoff, and metrics. |
| Provider differences make live tests flaky | Deterministic mocks in required CI; live tests isolated, scheduled/manual, and cleanup-verified. |
| Generated TypeScript output drifts | Source-only authority, deterministic build, and CI diff check. |
| Dual SDK suites drift | Shared declarative corpus and scenario IDs; runners remain thin adapters. |

## 8. Handoff order for engineering

1. **Contract owner:** land R0 and obtain review of the amended contract/ADR.
2. **Test owner:** land R1’s pinned corpus and baseline characterization.
3. **Storage owner:** land R2, then R3; do not begin broad handler rewrites before the backend contract suite is green.
4. **Protocol owner:** land R4 in parallel, then coordinate R5/R6.
5. **S3 owner:** complete R5/R6 and make all local conformance scenarios green.
6. **Run-through owner:** complete R7/R8 against deterministic mocks.
7. **Package owner:** complete R9 against the stable server behavior.
8. **Release owner:** complete R10, run R11, and publish only after the acceptance checklist is complete.

The first implementation session should begin with R0 and R1 only. Foundation refactoring is not complete merely because interfaces compile; it is complete when both backends pass the same behavioral contract suite and the SDK corpus runs through both clients.
