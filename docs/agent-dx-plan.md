# Stow Agent DX and Python Plan

**Status:** proposed
**Last updated:** 2026-09-25
**Scope:** open-source package evolution for short-lived agents and developer workflows
**Relationship to existing work:** additive to `docs/remediation-plan.md`; the remediation release remains the foundation and release gate for this work

## 0. Revision note and current state

This plan was first drafted against an earlier baseline and has since been
reviewed against the repository as it stands. Several things it scheduled as
future work already exist, and two safety items were mis-scoped. The phases
below are reordered accordingly.

### 0.1 Already built — do not rebuild

| Area | Status | Where |
|---|---|---|
| Native S3 through one runtime facade | Done. Every native S3 request passes through a single runtime instance, so the server has a real quota and accounting choke point. | `internal/runtime/adapter.go`, `cmd/stow-s3/runtime_store.go` |
| In-process S3 compatibility adapter | Done, through multipart, conditionals, checksums, and pagination. | `internal/runtime/adapter.go`, `internal/runtime/multipart.go` |
| Durable outbox | Done. Cross-process claims, lease renewal, fencing, prepared-owner recovery, crash reconciliation against upstream, and a schema-version guard. | `internal/runthrough/file_outbox.go`, `outbox_claims.go`, `outbox_adapter.go` |
| Shared conformance corpus | Done. `conformance/corpus/cases.json` is the single source of truth for both the Go and Node runners. | `conformance/`, `packages/stow-s3/test/shared-corpus.ts` |
| Live provider matrix | Done. Disposable-resource live run-through with provider classification and a release gate. | `.github/workflows/live.yml`, `conformance/live-provider.sh` |
| Embedded profiles | Done, including the IndexedDB browser persistence profile. | `packages/stow-s3/src/embedded.ts`, `packages/stow-s3/src/browser.ts` |
| One version source | Done and enforced in CI. | `scripts/check-version.mjs` |
| Missing-bucket consistency | Done. Both backends return `ErrBucketNotFound` and the shared contract suite asserts it. | `internal/storage/backend_contract_test.go` |
| Data-directory reset is ownership-checked | Done. A reset requires a `.stow-owner` marker the server writes on open, and refuses root, home, the working directory, and their ancestors. `cleanSlate` remains as a deprecated alias with identical checks. | `internal/storage/fs/owner.go`, `packages/stow-s3/src/ownership.ts`, `docs/adr/0006-owned-data-directory-reset.md` |
| Live writes require explicit consent | Done. A policy no longer grants consent; only `STOW_ALLOW_LIVE_WRITES` or `--allow-live-writes` does, and local mode is proven to make zero upstream requests. | `internal/runthrough/writepolicy.go`, `cmd/stow-s3/store.go`, `docs/adr/0005-live-write-requires-explicit-consent.md` |

### 0.2 Real gaps this plan must now carry

Verified open items in the current tree:

1. **No request-body limit exists.** `internal/s3api/request.go` calls
   `io.ReadAll(r.Body)` with no cap, and there is no `http.MaxBytesReader`
   anywhere. The `maxRequestBytes` capability in section 5.2 does not exist.
   This is a live denial-of-service surface, not a later enhancement.
2. **Native quotas are effectively unlimited.** `cmd/stow-s3/runtime_store.go`
   passes `MaxInt64` for bytes and objects. The enforcement machinery exists;
   only the configured values are missing.
3. **No read or write timeouts.** Only `IdleTimeout` is set, in
   `internal/s3api/server.go`.
4. **A clean install cannot start a session.** `packages/stow-s3/src/bin.ts`
   resolves the binary from `STOW_BIN`, the monorepo, or finally the bare
   string `"stow"` on `PATH`. The npm package ships the WASM artifact but no
   native binary. This is the largest risk in the plan and it gates the entire
   session stack.
5. **The ready protocol is still a text line.** `STOW_READY` on stdout, parsed
   by a regex in `packages/stow-s3/src/start.ts`, with credentials on stdout. No
   `--ready-fd` exists.

### 0.3 Consequences for this plan

- The architecture requested in 7.1 already exists. Section 7.1 is rescoped to
  configuration and the two missing limits rather than new machinery.
- Phase 0 no longer ends in ten written decisions. It closes the safety gaps
  and settles the distribution question with a spike, because quotas, latency
  targets, and the distribution model are empirical unknowns.
- The first TypeScript session is the probe for those unknowns, not the last
  step of a long pre-work phase.

### 0.4 Verified open items, checked against the tree

An audit of the code rather than of these documents produced the list below.
It is recorded because both this plan and `docs/agentic-dx-10-plan.md` had
started marking phases complete on the strength of prose, and two shipped
data-loss defects were found underneath a phase that read as done. Each item
names the evidence, so the next reader can re-check it rather than trust it.

Closed since this section was written:

1. **Live writes were granted implicitly.** `STOW_POLICY=mirrorWrites` set
   `AllowLiveWrites` whenever `STOW_ALLOW_LIVE_WRITES` was *unset*, so the
   absence of a variable was the enabling condition. With ADR 0001's auto-detect
   default, a staging `.env` was enough to propagate mutations to a shared
   bucket. Fixed; `docs/adr/0005-live-write-requires-explicit-consent.md`.
2. **`cleanSlate` was an unguarded recursive delete** of any caller-supplied
   path, including the home directory, in the published `dist/`. Fixed;
   `docs/adr/0006-owned-data-directory-reset.md`.

Still open, in descending order of harm:

3. **The admin surface has no credential.** `--allow-public-admin` is a bare
   boolean, and the routes it exposes include the destructive outbox
   `retry` and `discard` actions. There is no admin token, so
   `--allow-public-admin` means *unauthenticated* rather than *authenticated*.
   `internal/s3api/server.go`.
4. **CORS reflects any origin.** `internal/s3api/cors.go` echoes the request's
   `Origin` and, when absent, sends `*`. There is no allowlist and no
   `Vary: Origin`. `Config.CORSOrigins` is declared and never read anywhere, so
   the allowlist was designed and never wired.
5. **The macOS parent-death watch is a no-op.** `internal/parentwatch` opens a
   kqueue descriptor and `defer`-closes it the instant `Watch` returns; no
   goroutine ever services the registered `NOTE_EXIT` event. macOS arm64 is a
   first-class release platform by the decision in section 17, so sessions can
   orphan there. Windows is `ErrUnsupported`, and `parentwatch_test.go` has no
   build tag, so it does not even compile on that platform. There is no macOS or
   Windows CI job.
6. **The filesystem key limit is ~127 bytes, not the documented 1024.**
   `storage.ValidateKey` accepts 1024, but keys become
   `hex.EncodeToString` filenames and `NAME_MAX` is 255, so a 128-byte key fails
   `ENAMETOOLONG` on every supported filesystem. The Phase 2 exit criterion names
   128-byte keys, and no test anywhere covers a long key.
7. **The TypeScript client ignores two of its own options.** `timeoutMs` and
   `signal` are declared on `EphemeralStowOptions` and never forwarded; startup
   is bounded by a hardcoded 10 s. The ready-descriptor parser also treats a
   partial record as complete, and a parse failure inside a stream handler
   rejects nothing, so a truncated record hangs until the timeout. The server's
   reported region is parsed and then overwritten with `us-east-1`.

Item 6 is the one that invalidates a stated exit criterion rather than merely
adding work, and it is also the prerequisite for the storage format v3 in the
10-plan: that change is a hash-filename scheme, which is the same fix.

## 1. Product outcome

Stow should become the shortest path from an agent or test to a disposable, S3-compatible workspace:

```text
install package -> acquire session -> use an S3 SDK -> run work -> release session
```

A successful session should provide:

- an automatically provisioned bucket;
- a configured S3 client for the language in use;
- a loopback endpoint when S3 wire compatibility is required;
- isolated credentials and data;
- predictable cleanup after success, failure, cancellation, or process termination;
- bounded resource usage;
- explicit capabilities;
- no ambient environment mutation;
- no manual bucket, credential, port, or process management for the common path.

The product is open source. Monetisation is not a release criterion. Adoption criteria are installation success, time to first operation, repeated use, reliability under parallel sessions, and the number of external agent/test workloads that can use the published packages without tribal knowledge.

## 2. Product thesis and audience

### Primary users

1. **Agent harness authors** who need a temporary S3-shaped workspace for each task, tool call, or child process.
2. **Test authors** who need S3 behavior in local and CI tests without cloud credentials or network dependencies.
3. **Library and application developers** who want a deterministic local S3 endpoint for development.
4. **Agent platform teams** that need an explicit temporary-resource lease and cleanup contract.

### Primary job

Give one short-lived execution context a private, disposable S3 workspace.

The caller should not have to learn the server lifecycle. The caller should use an existing S3 SDK, such as:

- AWS SDK for Go v2;
- `@aws-sdk/client-s3` for Node.js and TypeScript;
- `boto3` for Python;
- an S3-compatible client already used by the application.

Stow should not require users to adopt a new object-storage abstraction. The high-level package should be a lifecycle and provisioning layer around the existing S3 contract.

### Secondary job

Expose explicit advanced profiles for:

- persistent local filesystem storage;
- upstream read-through caching;
- controlled upstream writes;
- direct in-process Go/WASM storage;
- connecting to an externally managed Stow endpoint.

These profiles must be explicit. The ephemeral agent profile must not silently inherit upstream configuration or filesystem data.

### Non-audience

The first release of this direction is not:

- a production object-storage service;
- a multi-tenant SaaS control plane;
- a billing product;
- a replacement for AWS S3, MinIO, or LocalStack;
- a distributed object store;
- a general workflow engine.

## 3. Design principles

1. **One obvious happy path.** `withStow(...)` in TypeScript and `with stow.session()` in Python should be the first interface users see.
2. **S3 compatibility at the seam.** Language packages should configure existing S3 clients. They should not reimplement S3 operations.
3. **Ephemeral by default.** The standard agent session uses memory, one generated bucket, loopback-only networking, and automatic cleanup.
4. **Explicit ownership.** Every process, client, directory, lock, and background worker has one owner and one close path.
5. **No ambient configuration.** The default session must not inherit `STOW_*`, `S3_*`, or `AWS_*` values from the parent process.
6. **Capability negotiation.** A caller can discover persistence, multipart, upstream, quota, and protocol capabilities before issuing operations.
7. **Safe failure.** Startup, cancellation, quota, and cleanup failures are structured and actionable. AWS SDK errors remain recognizable.
8. **Deterministic isolation.** A session never shares buckets, credentials, cache state, or outbox state with another session.
9. **Bounded work.** Request size, object size, total bytes, object count, multipart staging, concurrency, and cache growth have explicit limits.
10. **Deep modules.** The public interface should hide process management, credentials, readiness, cleanup, and storage policy behind a small interface. The implementation may contain internal modules and adapters.
11. **Compatibility evidence.** Every advertised S3 operation has a shared conformance scenario across supported clients and backends.
12. **Additive migration.** Existing `Stow.start()`, `Stow.connect()`, and `EmbeddedStow` remain available while the scoped APIs mature.

## 4. Architecture decision

### 4.1 Default implementation: managed child S3 endpoint

The default agent session should use the existing Go server as a short-lived child process:

```text
TypeScript/Python package
        |
        v
  SessionManager
        |
        v
  Go child process
        |
        v
  local S3 HTTP endpoint
        |
        v
  memory backend
```

This is the default because it preserves the existing S3 wire contract, SigV4 authentication, AWS SDK interoperability, multipart behavior, and process-level fault isolation.

Each default session should use:

- `mode=local`;
- `backend=memory`;
- an ephemeral loopback port;
- generated local credentials;
- one automatically created bucket;
- no inherited upstream configuration;
- no persistent data directory unless explicitly requested.

### 4.2 Direct embedded runtime remains a separate profile

The direct Go/WASM runtime remains valuable for callers that:

- already run in the same process;
- do not need S3 wire compatibility;
- want zero listeners and credentials;
- want direct quotas and reset semantics.

`EmbeddedStow` should not pretend to be an S3 server. The in-process S3
compatibility adapter now exists for the native path as `runtime.StoreAdapter`,
which is what gives the HTTP server a single choke point for quotas and
accounting. The public `EmbeddedStow`/`@chs/stow-s3/browser` profiles remain
direct object interfaces with no S3 wire surface, and that split is intentional.

### 4.3 Do not start with a shared daemon

A shared daemon or multi-agent server adds session routing, leases, TTLs, heartbeats, authentication mapping, backpressure, orphan detection, and a larger security surface. The current code has one store and one auth function per server, not a multi-session router.

Build a prewarmed child pool only after measurements show that per-process startup or memory density violates a defined target. Keep the pool behind the same `Session` seam.

### 4.4 Module map

The implementation should converge on these modules:

| Module | Responsibility | Primary adapters |
|---|---|---|
| `SessionManager` | Acquire and release one isolated session | managed child process, embedded runtime |
| `ReadyProtocol` | Exchange versioned startup capabilities and connection details | stdout/pipe/file descriptor |
| `ManagedProcess` | Spawn, wait, drain, signal, and reap the Go process | local CLI binary, packaged binary |
| `S3ClientFactory` | Construct a configured language SDK client | AWS SDK v2/v3, boto3, async boto |
| `ResourceBudget` | Enforce request, object, session, multipart, and cache limits | HTTP server, memory store, cache |
| `FixtureStore` | Apply declarative setup and reset state | memory/filesystem stores |
| `SessionHandoff` | Produce safe environment/config for a child agent | environment mapping, JSON handoff |
| `CapabilitySet` | Report and validate supported behavior | local, embedded, run-through |
| `ConformanceCorpus` | Define SDK-observable scenarios | Go, Node, Python, raw HTTP runners |

The language packages should depend on the session protocol and client factory, not on `internal/storage` or `internal/runthrough` implementation details.

## 5. Public session contract

All language adapters should implement the same semantics even when their syntax differs.

### 5.1 Common session lifecycle

`acquire()` must:

1. resolve the runtime binary or embedded artifact;
2. create an owned temporary directory if one is needed;
3. construct an isolated environment;
4. start the runtime with local-only defaults;
5. wait for a versioned ready message;
6. construct the language S3 client;
7. create the session bucket;
8. return a session context.

The callback or caller must not run until the bucket and client are ready.

`release()` must:

1. stop accepting new work;
2. destroy language clients;
3. stop background workers;
4. gracefully shut down the child;
5. force-kill after a bounded deadline if necessary;
6. remove only directories created by the session;
7. release locks and temporary state;
8. preserve the primary callback error if cleanup also fails.

`close()` must be idempotent. Operations after close must return a stable closed error.

### 5.2 Default session capabilities

The default session should report at least:

```json
{
  "backend": "memory",
  "persistent": false,
  "multipart": true,
  "upstream": false,
  "maxBytes": 67108864,
  "maxObjects": 10000,
  "maxRequestBytes": 8388608,
  "protocolVersion": 1
}
```

The exact defaults are a decision for Phase 0. The important rule is that limits must be enforced rather than merely reported.

### 5.3 TypeScript interface

Add an additive high-level interface to `packages/stow-s3/src`:

```ts
import type { S3Client } from "@aws-sdk/client-s3";

export interface StowSession {
  readonly s3: S3Client;
  readonly bucket: string;
  readonly endpoint: string;
  readonly capabilities: StowCapabilities;
  handoff(): Record<string, string>;
  close(): Promise<void>;
}

export interface EphemeralStowOptions {
  readonly maxBytes?: number;
  readonly maxObjects?: number;
  readonly timeoutMs?: number;
  readonly signal?: AbortSignal;
  readonly binary?: string;
}

export function withStow<T>(
  use: (session: StowSession) => T | Promise<T>,
  options?: EphemeralStowOptions,
): Promise<T>;

export function openStow(
  options?: EphemeralStowOptions,
): Promise<StowSession>;
```

The callback receives a client already configured with:

- endpoint;
- credentials;
- region;
- path-style routing;
- the Stow unsigned-payload middleware.

The primary interface should not require the caller to call `destroy()`, create a bucket, parse `STOW_READY`, or stop a process.

Keep these existing surfaces:

```ts
Stow.start(options): Promise<StowInstance>;
Stow.connect(options): StowConnection;
EmbeddedStow.open(host, options): EmbeddedStow;
```

`Stow.start()` remains the advanced process-owned API. `Stow.connect()` remains the external endpoint API. `EmbeddedStow` remains the explicit host bridge.

### 5.4 Python interface

Create a Python package under `packages/stow-s3-py/` with a `pyproject.toml` and a `src/stow/` import package. The PyPI distribution name is a decision for Phase 0; do not assume that `stow` is available.

Proposed public interface:

```python
from contextlib import contextmanager
from typing import Any, Iterator, Protocol


class StowSession(Protocol):
    bucket: str
    endpoint: str
    s3: Any
    capabilities: Any

    def handoff(self) -> dict[str, str]:
        ...

    def close(self) -> None:
        ...


@contextmanager
def session(
    *,
    max_bytes: int | None = None,
    max_objects: int | None = None,
    timeout_s: float = 10.0,
    binary: str | None = None,
) -> Iterator[StowSession]:
    ...


def open_session(
    *,
    max_bytes: int | None = None,
    max_objects: int | None = None,
    timeout_s: float = 10.0,
    binary: str | None = None,
) -> StowSession:
    ...
```

Usage:

```python
from stow import session


def run_agent() -> None:
    with session(max_bytes=8 * 1024 * 1024) as env:
        env.s3.put_object(
            Bucket=env.bucket,
            Key="input.json",
            Body=b'{"task": "summarize"}',
        )
        result = env.s3.get_object(
            Bucket=env.bucket,
            Key="input.json",
        )
```

The core Python package should use the standard library for process and protocol handling. Make `boto3` an optional dependency:

```text
stow[boto3]
```

Add async support only after the synchronous lifecycle is stable. A later extra may provide `aioboto3` or `aiobotocore`; async cleanup must close both the client context and the underlying process session.

### 5.5 Child-process handoff

Some agents run in a separate process and need environment variables rather than an in-process client. `handoff()` should return a copy, never mutate the parent environment:

```python
with session() as env:
    child_env = env.handoff()
    # Pass child_env to the child process.
```

The returned mapping should contain the endpoint, generated credentials, region, and bucket in a documented, redacted-by-default shape. The same handoff operation should be available from TypeScript. The language packages should not expose raw process handles in the default session interface.

### 5.6 Error model

Define structured errors for package lifecycle failures without hiding normal S3 errors.

```python
class StowError(Exception):
    code: str
    cause: Exception | None
```

Suggested codes:

- `startup`;
- `closed`;
- `cancelled`;
- `quota_exceeded`;
- `invalid_options`;
- `capability_mismatch`;
- `binary_not_found`;
- `protocol_mismatch`;
- `backend_error`;
- `internal`.

AWS errors such as `NoSuchKey`, `NoSuchBucket`, `AccessDenied`, and `PreconditionFailed` should pass through the normal SDK client unchanged. The session package should add context only when the failure concerns session acquisition or release.

## 6. Machine-readable readiness protocol

The current `STOW_READY` text line remains supported for compatibility. Add a versioned machine-readable channel for new language clients.

### 6.1 Protocol shape

```json
{
  "protocolVersion": 1,
  "binaryVersion": "0.3.0",
  "endpoint": "http://127.0.0.1:43127",
  "region": "us-east-1",
  "accessKeyId": "generated-access-key",
  "secretAccessKey": "generated-secret-key",
  "mode": "local",
  "backend": "memory",
  "capabilities": {
    "persistent": false,
    "multipart": true,
    "upstream": false,
    "conditionalWrites": true,
    "presignedUrls": true,
    "maxBytes": 67108864,
    "maxObjects": 10000,
    "maxRequestBytes": 8388608
  }
}
```

### 6.2 Transport options

Prefer a dedicated file descriptor or inherited pipe:

```text
stow serve --ready-fd 3
```

The child writes exactly one JSON object to that descriptor. Normal logs go to stderr. Credentials must not be repeated in ordinary request logs.

The protocol must support:

- a timeout while waiting for readiness;
- explicit cancellation;
- protocol-version mismatch errors;
- binary-version mismatch diagnostics;
- capability negotiation;
- no parsing of human-formatted log output.

### 6.3 `stow doctor`

Add a `stow doctor` command that reports:

- resolved binary path;
- binary version and protocol version;
- supported platform;
- writable temporary directory;
- available backend;
- available S3 client configuration;
- whether a test endpoint can bind and answer health checks.

This command gives Python and TypeScript users an actionable diagnostic without requiring them to inspect stack traces.

## 7. Safety and resource requirements

These are prerequisites for a trustworthy agent package. They are not optional production features.

### 7.1 Session limits

Enforce limits in the server, not only in the direct embedded runtime:

- maximum request body;
- maximum single-object size;
- maximum total session bytes;
- maximum object count;
- maximum multipart staged bytes;
- maximum multipart part count;
- maximum concurrent requests;
- maximum cache bytes and objects;
- bounded retry and shutdown windows.

The accounting seam this section originally asked for already exists: native S3
requests are mediated by one runtime instance, so object-count and byte quotas
are enforced in a single place. What remains is narrower than it looks:

- the request-body cap does not exist at all and must be added at the HTTP
  boundary before any session work;
- the native server currently passes unlimited quota values, so the enforcement
  is present but disabled;
- multipart staging and request-concurrency limits are not yet expressed;
- authentication, checksum validation, and SDK serialization can still create
  additional copies above the configured byte limit, which the target in 7.2
  addresses.

### 7.2 Body handling

Current request handling buffers bodies for content length, authentication, MD5/checksum validation, and storage. Add a single-pass path that:

1. enforces the request limit while reading;
2. streams to a temporary object record;
3. computes ETag/checksum during the write;
4. atomically commits metadata and bytes;
5. restores or closes temporary state on failure.

The memory backend may retain an in-memory object, but it must never exceed its session budget.

### 7.3 Filesystem safety

- Use private directory permissions for session data.
- Keep encoded object names within filesystem filename limits.
- Use a reversible key encoding that supports keys up to the documented limit.
- Check bucket existence before object lookup.
- Recover or report stale store locks after abnormal termination.
- Remove only session-owned temporary directories.
- Never delete a caller-owned directory during cleanup.

### 7.4 Process safety

- Do not pass credentials in process arguments.
- Sanitize inherited upstream and AWS variables by default.
- Drain stdout and stderr for the entire child lifetime.
- Kill and reap the child after cancellation or parent failure.
- Align Node/Python shutdown deadlines with the Go shutdown budget.
- Add a parent-death watcher on supported platforms.
- Do not build a shared daemon until parent-death and lease behavior are proven.

### 7.5 Network and admin safety

- Keep admin and metrics routes loopback-only by default.
- Require a separate admin credential before exposing destructive outbox actions remotely.
- Do not reflect arbitrary origins without an explicit allowlist and `Vary: Origin`.
- Add read and write timeouts to the HTTP server.
- Redact object keys and credentials from normal logs and metrics.
- Bound metric label cardinality.

## 8. Compatibility and conformance strategy

The compatibility corpus becomes the source of truth for the package interfaces.

### 8.1 Required clients

Run the same scenarios through:

- AWS SDK for Go v2;
- AWS SDK v3 for Node.js;
- `boto3` for Python;
- a raw HTTP runner for authentication, routing, and safety edges.

Run local scenarios through:

- memory backend;
- filesystem backend.

Run advanced scenarios through:

- mock upstream;
- opt-in disposable live provider.

### 8.2 Required scenario groups

1. unsigned, malformed, expired, and presigned authentication;
2. bucket lifecycle and idempotency;
3. object create, read, update, delete, and copy;
4. opaque keys, encoded separators, repeated slashes, and long keys;
5. conditional GET, HEAD, PUT, and copy;
6. MD5, CRC32, CRC32C, SHA-1, and SHA-256;
7. range reads and invalid ranges;
8. list pagination, prefixes, delimiters, and continuation tokens;
9. multipart initiation, upload, list, complete, abort, and wrong-part errors;
10. quotas and request-size rejection;
11. session cancellation and cleanup;
12. run-through cache miss, hit, revalidation, stale-on-error, and 404 eviction;
13. durable outbox persistence, crash recovery, retry, and discard;
14. unsupported S3 markers and capability errors;
15. package acquisition from a clean installed artifact.

### 8.3 Corpus requirements

Each scenario should declare:

- stable ID;
- client and backend matrix;
- setup;
- operation;
- expected status and error code;
- required headers and metadata;
- cleanup requirements;
- whether it is a release gate or an explicitly deferred non-goal.

A skipped required scenario is a failure. A live-provider scenario may be scheduled, but its result must be recorded for the release commit.

## 9. Delivery plan

The phases are ordered by dependency, not by language. The first implementation slice should be Phase 0 and Phase 1, not a broad S3 expansion.

### Phase 0 — Close the safety gaps and settle distribution

**Priority:** P0
**Dependencies:** none
**Outcome:** no unbounded request path, and a measured answer to "can a clean
install start a session?"

This phase replaces the original document-only Phase 0. The decisions that phase
collected — default quotas, latency targets, the distribution model — are
empirical. They are settled here by measurement and a spike, not by argument.

Work:

1. Add a request-body cap at the HTTP boundary with an S3-visible error, and
   cover it with a corpus case.
2. Add read and write timeouts alongside the existing idle timeout.
3. Replace the unlimited native quota values with configured limits and flags,
   and prove enforcement with a test at the boundary.
4. Express multipart staging and request-concurrency limits, or record them as
   explicitly accepted non-goals for the first release.
5. Spike binary distribution: measure whether a clean-room `npm pack` install
   can obtain, verify, and launch a per-platform binary. Record the result as a
   short decision note, including the outcome if the answer is no.
6. Spike the Python distribution model against the same question, including
   wheel feasibility per platform.
7. Record a benchmark baseline on a named machine: time to ready, time to first
   operation, shutdown time, and peak RSS. Section 11 targets are adopted only
   after this baseline exists.
8. Define the session lifecycle, error codes, ready-protocol fields, and
   ownership rules as an ADR, now that the unknowns are measured.
9. Confirm the default profile promise: disposable, local-only, memory-backed,
   no inherited configuration.

Exit criteria:

- an oversized request is rejected before allocation exceeds the limit;
- native session quotas are enforced and tested;
- the distribution spike has a written answer and a chosen model;
- a benchmark baseline exists, so section 11 targets are measurements rather
  than aspirations;
- the default session cannot inherit upstream configuration accidentally;
- the session contract is recorded in `docs/adr/0004-agent-session-contract.md`,
  including the default limits, the ready protocol, the error codes, and what is
  explicitly deferred.

**Status: complete.** The request cap, timeouts, quota flags, distribution
spike, and baseline have landed. The one item deliberately not implemented is
multipart staging and request-concurrency limits, recorded as an explicit
non-goal in the ADR with its reasoning: each request is already bounded and the
remaining exposure is unbounded request *count*, so a semaphore added now would
be untested policy rather than a measured safeguard.

### Phase 1 — Build the shared session module and ready protocol

**Priority:** P0
**Dependencies:** Phase 0
**Outcome:** language-neutral acquisition and release semantics, proven by one
thin end-to-end slice rather than a completed module set.

This phase deliberately ends with a working single-session round trip, because
that is the cheapest probe of the unknowns left after Phase 0.

Work:

1. Add a versioned ready JSON channel behind `--ready-fd`, while preserving
   `STOW_READY` text output for existing consumers. Move credentials off stdout.
2. Extend the existing child-process handling in `packages/stow-s3/src/start.ts`
   with the JSON ready path rather than building a parallel spawner.
3. Add a session-owned temporary directory implementation.
4. Add context, timeout, and cancellation support.
5. Add parent-death and orphan-reaping behavior.
6. Add capability validation before returning a session.
7. Ship the first working `withStow()` as a thin vertical slice: one callback,
   one configured client, one created bucket, deterministic teardown.
8. Add `stow doctor`.
9. Only then extract a `SessionManager` interface, if the slice shows a real
   seam. Do not build the abstraction first.
10. Run a 100-session lifecycle test against the slice and record the numbers.

Exit criteria:

- a client can acquire a session without knowing how the child is started;
- one TypeScript callback runs a full put/get round trip;
- a failed startup leaves no process, lock, or temporary directory;
- cancellation has deterministic cleanup;
- 100 sequential sessions have zero leaked resources, and 100 parallel sessions
  have unique endpoints, credentials, buckets, and directories.

### Phase 2 — Add resource guardrails and fix storage correctness blockers

**Priority:** P0
**Dependencies:** Phase 1
**Outcome:** an agent cannot exhaust the host process or hit avoidable backend errors.

Work:

1. Add a shared limits configuration and defaults, informed by the Phase 0
   benchmark baseline.
2. Add multipart staging and concurrency limits, if Phase 0 did not record them
   as accepted non-goals.
3. Add a streaming write path for large objects, so the body is not resident in
   full. **Do not estimate this item until the precondition is answered:** can
   SigV4 payload signing and `Content-MD5` be computed incrementally over a
   streaming body? Scored 0.46, so it is genuinely open. The "single-copy"
   variant that previously shared this item is withdrawn; it was built and
   measured to *raise* peak RSS, because the copy it removed was what kept the
   collector running.
4. Verify filesystem key-length handling against the documented limit.
5. Add stale-lock recovery or a clear operator recovery command.
6. Add tests for concurrent writes, cancellation, and resource cleanup.

Completed in Phase 0 and not repeated here: the request-body cap, native quota
wiring, read/write timeouts, and missing-bucket consistency, which the shared
storage contract suite already enforces for both backends.

Exit criteria:

- oversized input fails before storage allocation exceeds the configured limit;
- 128-byte and maximum-length object keys behave according to the documented contract;
- no request can cause unbounded memory growth in the default session. This is
  already enforced by the 16 MiB byte quota, the 1,000 object quota and the 8 MiB
  per-request body cap, all wired to the native runtime and covered by
  end-to-end tests, so it is a regression assertion here rather than outstanding
  work; what remains unproven is the bounded per-session figure in section 11;
- no temporary file remains after failed or cancelled operations.

### Phase 3 — Ship the TypeScript agent API

**Priority:** P0
**Dependencies:** Phases 0–2
**Outcome:** a one-call TypeScript DX.

Work:

1. Implement `withStow()` with automatic bucket creation and cleanup.
2. Implement `openStow()` for manual lifetime control.
3. Return a configured S3 client and capability set.
4. Add `handoff()` for child-agent processes.
5. Add `AbortSignal` support.
6. Keep `Stow.start()` and `Stow.connect()` backward compatible.
7. Keep `EmbeddedStow` as an explicit low-level API.
8. Add a test helper for common TypeScript test runners.
9. Add package-level examples for test and agent usage.
10. Add lifecycle tests for startup failure, callback failure, cancellation, repeated close, and concurrent sessions.

Exit criteria:

- a new TypeScript user can run a put/get round trip in one callback;
- no user code handles credentials, ports, buckets, or process teardown;
- existing TypeScript users retain current advanced APIs;
- Node 20, 22, and 24 pass the package and lifecycle suites;
- package tests run against the published or packed artifact, not only the monorepo.

### Phase 4 — Ship the Python agent API

**Priority:** P0
**Dependencies:** Phases 0–2
**Outcome:** a one-call Python DX for boto3-based agents and tests.

Work:

1. Create `packages/stow-s3-py/` with a standard `src/` layout and `pyproject.toml`.
2. Implement the standard-library process and protocol client.
3. Implement `session()` and `open_session()`.
4. Return a configured `boto3` client from the `boto3` extra.
5. Add `handoff()` for child processes.
6. Add timeout, cancellation, and idempotent close behavior.
7. Add a pytest fixture.
8. Add packaging tests from a clean virtual environment.
9. Add a binary-resolution diagnostic and `stow doctor` integration.
10. Add sync tests against the shared corpus.
11. Add async design spike before committing to `aioboto3` or `aiobotocore` as a public dependency.

Exit criteria:

- a Python user can run a boto3 round trip in one context manager;
- cleanup occurs on normal return, exception, `KeyboardInterrupt`, and cancellation;
- no parent environment variable is mutated;
- a clean wheel or sdist can locate or install a supported binary;
- Python and TypeScript clients consume the same ready protocol;
- the Python package does not import Go internals or duplicate S3 semantics.

### Phase 5 — Make distribution self-contained and diagnosable

**Priority:** P0
**Dependencies:** Phases 3–4
**Outcome:** a stranger can install the package and start a session without a monorepo.

Work:

1. Choose the npm distribution model: bundled platform artifacts or explicit companion binary packages.
2. Choose the PyPI distribution model: wheels with platform artifacts, optional binary packages, or an explicit installer.
3. Include the WASM artifact and loader only if the embedded profile is publicly supported.
4. Include license and package metadata in every artifact.
5. Add `stow version` and protocol version reporting.
6. Add `stow doctor`.
7. Test `npm pack` followed by installation into a clean project.
8. Test Python wheel/sdist installation into a clean virtual environment.
9. Test native artifacts on every supported OS/architecture.
10. Decide whether Windows is supported before promising it in package metadata.
11. Publish checksums, provenance, and a machine-readable release manifest.
12. Make release jobs verify that the binary and language packages come from the same commit.

Exit criteria:

- a clean install can start a default session;
- missing or incompatible binaries produce actionable diagnostics;
- every published artifact runs on its declared platform;
- no package claims to contain a binary unless it does;
- release metadata identifies the exact binary and protocol versions.

### Phase 6 — Expand conformance and compatibility evidence

**Priority:** P0
**Dependencies:** Phases 1–5
**Outcome:** advertised S3 behavior is measured, not assumed.

Work:

1. Convert the existing corpus into named language-neutral scenarios.
2. Add Go, Node, and Python runners.
3. Add raw HTTP safety scenarios.
4. Add filesystem and memory matrices.
5. Add lifecycle and cleanup scenarios.
6. Add package-install scenarios.
7. Add capability mismatch scenarios.
8. Add unsupported-operation scenarios with stable errors.
9. Add a trace table from public interface claims to scenario IDs.
10. Make every required scenario fail CI when skipped.

Exit criteria:

- all required local scenarios pass for every supported client;
- all package acquisition paths pass from clean installs;
- unsupported operations fail predictably;
- the release contains a machine-readable conformance report.

### Phase 7 — Harden run-through as an advanced profile

**Priority:** P1
**Dependencies:** Phases 1–6
**Outcome:** upstream behavior is safe enough for controlled agent workflows.

Work:

1. Keep run-through disabled for the default session.
2. Require explicit run-through selection and live-write opt-in.
3. Make upstream configuration explicit rather than auto-detected for ephemeral sessions.
4. Add durable intent-before-commit or a proven reconciliation protocol.
5. Make local commit, cache invalidation, and outbox state observable as one transaction.
6. Add bounded cache configuration and eviction metrics.
7. Bound merged listings instead of materializing every page.
8. Define upstream-only versus local-shadow bucket behavior.
9. Add mock-provider scenarios for 404, throttling, permission failure, transient failure, and crash recovery.
10. Run opt-in disposable live tests for supported providers.

Exit criteria:

- local data cannot be silently lost during propagation failure;
- stale cache data cannot appear after a successful local delete;
- outbox retries preserve per-key ordering;
- live tests use disposable resources and short-lived credentials;
- run-through failures have stable S3-visible errors.

### Phase 8 — Improve the embedded and WASM profiles

**Priority:** P1
**Dependencies:** Phases 1–2
**Outcome:** embedded callers get the same lifecycle clarity without pretending to have S3 wire support.

Work:

1. Add a scoped embedded adapter with the same close/reset semantics as the managed session.
2. Publish a self-contained WASM loader and runtime artifact if the profile remains public.
3. Add explicit capability reporting for multipart, persistence, upstream, and quotas.
4. Decide whether embedded S3 compatibility is required. If yes, build an in-process S3 adapter; if no, document the direct object interface as the supported contract.
5. Add Node, browser, and Go host tests appropriate to the supported environments.
6. Add package-level embedded examples.
7. Ensure WASM close releases callbacks and terminates the module cleanly.

Exit criteria:

- embedded and managed profiles have clear, non-overlapping capability contracts;
- no public profile silently falls back to another backend;
- embedded cleanup is deterministic;
- the public package contains every artifact required by its documented profile.

### Phase 9 — Agent and test ecosystem integrations

**Priority:** P1
**Dependencies:** Phases 3–6
**Outcome:** agents can use Stow without writing lifecycle glue.

Work:

1. Add a pytest fixture and a TypeScript test helper.
2. Add a small set of agent-framework adapters only after the core session API is stable.
3. Add declarative fixture manifests for input objects and expected outputs.
4. Add automatic cleanup of multipart uploads, cache state, and outbox state on reset.
5. Add a subprocess handoff example.
6. Add examples for parallel test workers and CI jobs.
7. Add a capability-aware agent tool description or metadata format.
8. Collect opt-in feedback on missing integration points.

Exit criteria:

- a target agent can acquire a session using one adapter call;
- a test suite can use the package without writing process cleanup;
- framework adapters do not duplicate session semantics;
- examples run in CI against the published package.

### Phase 10 — Release, observability, and external validation

**Priority:** P0 for release, P1 for ecosystem
**Dependencies:** all implementation phases selected for the release
**Outcome:** a package outsiders can install, trust, and use repeatedly.

Work:

1. Add structured, redacted request and lifecycle logs.
2. Add bounded metrics for session count, startup latency, operation latency, quota rejection, cleanup, and outbox state.
3. Add security review for body-size denial of service, path handling, admin exposure, secret logging, and subprocess cleanup.
4. Add dependency scanning, SBOM generation, artifact provenance, and pinned CI actions.
5. Run clean-install pilots with at least five representative agent/test workloads.
6. Measure time to first operation, manual workarounds, parallel reliability, memory use, and failure recovery.
7. Publish examples and a capability matrix.
8. Establish a semver and compatibility policy before the first stable package line.
9. Publish the first agent-DX release only after package-install and clean-environment gates pass.

Exit criteria:

- five external pilots complete the core job from published artifacts;
- no pilot requires tribal lifecycle knowledge;
- no orphaned process or temporary directory appears in lifecycle/soak tests;
- security and dependency gates have no unaccepted high-severity findings;
- the release report includes performance, conformance, and pilot results.

## 10. Prioritized backlog

| ID | Priority | Work item | Depends on | Exit condition |
|---|---:|---|---|---|
| A0 | P0 | Safety gaps: body cap, native quota wiring, read/write timeouts | — | Oversized request rejected before allocation; quotas enforced and tested |
| A0.5 | P0 | Binary distribution spike and decision | A0 | Written answer: a clean install can start a session, with the chosen model |
| A1 | P0 | Versioned ready protocol behind `--ready-fd` | A0 | Credentials off stdout; Go and TS parse the same JSON message |
| A2 | P0 | Session lifecycle, cancellation, cleanup | A0–A1 | Scoped acquire/release works; 100 sessions leak nothing |
| A2.5 | P0 | Benchmark baseline recorded | A0 | Named machine, recorded p50/p95/RSS; section 11 targets adopted from it |
| A3 | P0 | Multipart staging and concurrency limits | A0, A2 | Limits reject work before unbounded allocation |
| A4 | P0 | Filesystem key-length regression tests | A3 | Maximum-length keys pass the documented contract |
| A5 | P0 | TypeScript `withStow` thin slice | A0.5–A3 | One callback runs a full S3 round trip |
| A6 | P0 | Python `session` | A1–A4 | One context manager runs a boto3 round trip |
| A7 | P0 | Clean package distribution | A0.5, A5, A6 | Packed npm/PyPI artifacts start cleanly |
| A8 | P0 | Python runner over the shared corpus | A1–A7 | All required scenarios pass across Go, Node, and Python |
| A9 | P1 | Safe run-through profile | A3, A8 | No lost or stale upstream state |
| A10 | P1 | Embedded/WASM package polish | A2, A3 | Public embedded lifecycle is self-contained |
| A11 | P1 | Agent/test integrations | A5–A8 | Target users need no lifecycle glue |
| A12 | P0 | Release and pilot gate | A7–A11 | Published package passes clean-install pilots |
| A13 | P2 | Async Python client | A6, A8 | Async context/client cleanup is proven |
| A14 | P2 | Prewarmed child pool | A2, A12 | Measurements justify and validate pooling |

Completed before this backlog opened, and therefore absent above: the native
runtime facade and its S3 adapter, the durable outbox with crash reconciliation,
the shared conformance corpus with Go and Node runners, the live provider
matrix, the embedded and browser profiles, the single version source, and
missing-bucket consistency across backends.

The critical path is:

```text
A0 -> A0.5 -> A1 -> A2 -> A3 -> A4 -> A5/A6 -> A7 -> A8 -> A12
```

A0 is first because it is a live safety surface and has no dependencies. A0.5
is second because every session decision is made against it: if a clean install
cannot start a session, the shape of A5 through A7 changes. A2.5 sits beside A2
so the targets in section 11 are derived from measurement rather than asserted.
A9, A10, A11, and A13 can run in parallel after their dependencies are stable.
A14 waits for measurement and does not block the first agent-DX release.

## 11. Testing strategy

### Unit tests

Test each module through its public interface:

- ready-message parsing and version validation;
- process acquisition, cancellation, and cleanup;
- environment sanitization;
- capability negotiation;
- quota accounting;
- fixture setup and reset;
- handoff generation;
- error translation.

### Integration tests

Run the same lifecycle through:

- TypeScript;
- Python;
- Go;
- a child agent process;
- an S3 SDK from each supported language.

Cover:

- one session;
- repeated sessions;
- 100 parallel sessions;
- startup failure;
- callback failure;
- cancellation;
- process kill;
- oversized requests;
- parent process death.

### Conformance tests

The corpus is the source of truth for S3 behavior. Each scenario has one ID and runs across the supported matrix. Do not maintain separate hand-written expectations for each language.

### Performance tests

Measure and publish:

- binary discovery time;
- process spawn time;
- time to ready;
- time to bucket creation;
- time to first S3 operation;
- shutdown time;
- peak RSS;
- memory per session;
- maximum concurrent sessions;
- large-object memory ceiling;
- cache and outbox growth.

**Baseline first.** Record a baseline on a named machine before adopting any
target below. The targets are decisions made against that baseline, not
assumptions made before it. If a measurement contradicts a target, change the
target or change the design, and record which.

A baseline now exists: `docs/benchmarks/session-baseline.md`, measured on a
4-core Intel i5-7600K with 15 GB RAM, Node 24, memory backend, over 30
sequential sessions. It measured 15.4 ms p50 time to ready, 41 ms p50 to first
operation, 35.5 ms p50 shutdown, 11.6 MB fixed process RSS, and 4.59 MB of peak
RSS per MiB of object data. The 4.59 figure is the starting point, not the
current one: it is 4.22 after two redundant live copies were removed, and 3.27
for a session, which now runs `GOGC=50`. The baseline document is kept current;
this paragraph records where the plan began.

Findings that changed this plan, in the order they were established. The first is
narrowed by the second, which supersedes the copy-count reasoning it was based
on:

- The proposed "peak memory overshoot < 20%" target is unreachable as written.
  A byte quota is not a memory bound while the write path buffers the body, so
  the target is restated as a bounded per-session peak RSS rather than a
  percentage of the quota.
- **The multiplier is set by the Go collector's headroom over the live set, not
  by how many copies of the body exist.** This was measured three times and the
  copy-count account was wrong each time. Collapsing four transient copies inside
  `s3api` into one moved the number less than the run-to-run spread. Removing two
  *simultaneously live* redundant copies moved it from 4.59 to 4.22. Letting the
  store adopt the caller's buffer, which removes the last live pair, made it
  **worse at every collector target** (4.24 to 4.65 at `GOGC=100`), because the
  copy it removed was the allocation pressure that kept the collector running
  often enough to hold the heap below its ceiling. The original "reduce 4.59x
  toward 1.5x once the write path is single-copy" is therefore withdrawn:
  single-copy cannot reach it, and going further in that direction moves the
  wrong way. `docs/benchmarks/session-baseline.md` sections 1a-1c have the
  measurements.
- **The lever that works is the collector target, and it is now shipped.** A
  session's own server runs `GOGC=50`, which measures 3.27 MB per MiB against
  4.19 at the Go default; `GOGC=20` reaches 2.96. The setting is on the session's
  child process only, so it cannot affect a long-lived server, and a `GOGC` the
  caller set wins over the default. Both clients are held to the same value by
  `scripts/check-version.mjs`.
- The 64 MiB default in section 5.2 implies roughly 300 MB of peak RSS per
  session at the measured multiplier, so 100 parallel sessions would need about
  30 GB. The recommended default is 16 MiB and 1,000 objects, keeping the 8 MiB
  per-request cap.

Initial targets, ratified against that baseline where noted:

| Metric | Target |
|---|---:|
| Time to ready, local developer machine, p50 | < 50 ms (measured 15.4 ms) |
| Time to first S3 operation, p95 | < 500 ms (measured 89 ms) |
| Session shutdown, p95 | < 1 s under normal load (measured 49 ms) |
| Orphan processes after 1,000 cancellations | 0 |
| Leaked session directories after 1,000 cancellations | 0 |
| 100 parallel default sessions | All acquire and release successfully, **on a host with at least 16 GB free**. At 16 MiB per session and the shipped `GOGC=50` the derived figure is roughly 6.5 GB. Not yet measured. |
| Peak RSS per session, default session | **≤ 65 MB**: 11.6 MB fixed process RSS plus a 16 MiB quota at the measured 3.27 MB per MiB for the shipped `GOGC=50`. Derived from measurements, not yet measured end to end on a session; to be asserted. |
| Peak RSS per MiB of stored object data | **No copy-count target.** The multiplier is set by collector headroom over the live set, and removing the last redundant copy was measured to make it worse. Near 1x requires not holding the body resident, which is unresolved. See `docs/benchmarks/session-baseline.md` §1c. |
| Required conformance scenarios skipped | 0 |

Targets are measured on a documented benchmark environment. They are not claims about every host.

### Soak tests

Run at least 24 hours with repeated:

- session acquisition/release;
- parallel uploads;
- quota rejection;
- cancellation;
- process termination;
- cache and outbox retries.

Check for:

- process leaks;
- file descriptor leaks;
- temporary directory growth;
- memory growth;
- stale locks;
- outbox growth;
- unbounded logs;
- incorrect cleanup.

## 12. Release and distribution plan

### 12.1 Versioning

Decide the next version boundary in Phase 0. The current remediation work and the agent session API must not silently change the meaning of existing flags or APIs.

Use one version source for:

- Go binary;
- TypeScript package;
- Python package;
- ready protocol;
- status endpoint;
- release manifest.

Add explicit protocol versioning independent of package versioning.

### 12.2 Native binary

Support only platforms that can be tested in CI. Decide explicitly whether Windows is in the first agent-DX release. Do not advertise a platform through package metadata before its artifact and clean-install test exist.

The binary should provide:

- `stow version`;
- `stow doctor`;
- `stow serve`;
- a machine-readable readiness channel;
- bounded shutdown;
- no credentials in normal logs.

### 12.3 npm

Choose one of:

1. platform-specific optional packages containing the binary; or
2. a documented companion installer that resolves a platform binary.

A bare npm install that cannot start a session is not an acceptable default for the agent DX release. The package must either contain the required artifact or provide a clear install-time/runtime diagnostic and documented resolution path.

**This is the first decision to make, and it is made by measurement, not
argument.** The A0.5 spike answers one question: can a clean-room `npm pack`
install obtain, verify, and launch a per-platform binary today? Selection
criteria for the spike's outcome:

- install success rate on each supported platform, measured rather than assumed;
- artifact size and install time cost of shipping the binary in every install;
- whether checksums and provenance can be verified before execution;
- whether the resolution order in `packages/stow-s3/src/bin.ts` can report a
  precise, actionable diagnostic when the binary is absent;
- what the fallback experience is on an unsupported platform.

### 12.4 PyPI

Choose one of:

1. platform wheels with bundled artifacts;
2. optional platform binary packages;
3. a thin client with an explicit `stow` installation prerequisite.

The first Python release should be tested from a clean virtual environment, not
only from the repository.

This is the highest-risk distribution decision in the plan, because option 1
requires a per-platform wheel build for every supported OS and architecture,
option 2 requires a second naming and publishing surface, and option 3 concedes
that a Python install does not produce a working session on its own. Phase 0
spikes it alongside the npm question, with the same success criteria: install
success rate per platform, artifact size, checksum and provenance verification
before execution, and the diagnostic quality when the binary is missing. The
distribution name on PyPI must be confirmed available before the package
structure is committed.

### 12.5 Release gate

A release cannot publish if any of these fail:

- Go build, vet, race tests, and standards;
- TypeScript build, tests, and generated-output check;
- Python build, tests, and clean-install check;
- shared local conformance corpus;
- raw HTTP safety tests;
- 100 parallel session lifecycle test;
- orphan and temporary-directory leak test;
- package-content verification;
- security/dependency scan;
- required live-provider run-through result when the release includes run-through changes.

## 13. Observability and supportability

The default package should be quiet enough for agent execution but diagnosable when it fails.

Expose:

- startup duration;
- session ID;
- binary and protocol version;
- selected profile;
- bucket count and object count;
- quota rejection counts;
- cleanup duration and result;
- cache/outbox state for advanced profiles;
- redacted request IDs;
- parent/child process failure reason.

Do not collect product analytics by default. Open-source adoption evidence should come from opt-in feedback, published issues, package usage, and external pilots rather than hidden telemetry in a local runtime.

## 14. Risks and mitigations

| Risk | Mitigation |
|---|---|
| Wrappers duplicate lifecycle logic | One versioned session protocol and shared conformance scenarios |
| Binary packaging becomes a platform project | Ship a thin client first, measure install success, then add platform artifacts |
| `boto3` becomes a mandatory heavy dependency | Keep it in an optional extra; keep the core package standard-library only |
| Async clients create resource leaks | Add async support only after explicit client/resource close semantics are tested |
| Agents inherit cloud configuration | Sanitize environment by default and report the effective profile |
| Child processes survive parent failure | Parent-death watcher, bounded shutdown, force-kill, and reap tests |
| S3 compatibility expands without limit | Pin a corpus and require an explicit contract change for new operations |
| Run-through writes damage real data | Default local-only, explicit live-write opt-in, disposable credentials, durable outbox |
| Quotas are reported but not enforced | Central limits module with storage and HTTP integration tests |
| Package names conflict | Decide npm/PyPI names in Phase 0 before publishing |
| External users cannot reproduce the demo | Clean-install pilots and capability matrix are release gates |
| Generated package output drifts | Source-only authority and CI diff/package-content checks |
| A shared daemon is added too early | Keep it behind the session seam and require measurements before implementation |

## 15. Explicitly deferred

Do not make these prerequisites for the first agent-DX release:

- full Amazon S3 parity;
- versioning;
- ACL and bucket-policy enforcement;
- lifecycle rules;
- replication;
- notifications;
- S3 Select;
- distributed storage;
- TLS termination and wildcard DNS;
- a hosted multi-tenant service;
- billing and subscriptions;
- a shared daemon;
- a custom Python object API that duplicates boto3;
- browser-specific storage features beyond the shipped IndexedDB profile;
- Bun support;
- automatic upstream bucket creation.

Add these only when external agent workloads demonstrate a need.

## 16. First execution slice

The next implementation session should not start with Python packaging or more
S3 operations, and it should not start with a document. It should close the
unbounded request path and answer the distribution question:

1. Add the request-body cap at the HTTP boundary, with an S3-visible error and a
   corpus case.
2. Add read and write timeouts.
3. Replace the unlimited native quota values with configured limits and prove
   enforcement at the boundary.
4. Run the clean-room distribution spike for npm and record the answer.
5. Record the benchmark baseline on a named machine.
6. Write the session/protocol ADR, now informed by those measurements.
7. Add the versioned ready protocol behind `--ready-fd`, with credentials off
   stdout.
8. Add the first thin `withStow()` slice and run the 100-session lifecycle test.
9. Only then add Python, clean packaging, conformance expansion, run-through,
   embedded packaging, async Python, and framework integrations.

The project becomes a strong agent and DX library when a new user can write one scoped block, use a normal S3 SDK, and trust that nothing survives the block unless they explicitly choose otherwise.

## 17. Decision checklist before implementation

- [ ] What is the exact default promise?
- [ ] What is the canonical session name in TypeScript and Python?
- [ ] What is the PyPI distribution name, and is it available? — **`stow-s3`**, import
  package `stow_s3`, Python 3.10+. `stow` is taken on PyPI by an unrelated
  package. Recorded in `docs/distribution-spike.md`.
- [ ] Which S3 client libraries are first-class?
- [ ] Is async Python part of the first release or a later release?
- [ ] Which platforms ship in the first release? — **macOS arm64 and Linux x64.**
  The release pipeline already builds all four candidate platforms.
- [ ] How is the binary distributed with each package? (A0.5 answer: platform
  optional packages. Measured in `docs/distribution-spike.md`.)
- [x] What are the default quotas? — **16 MiB / 1,000 objects / 8 MiB per
  request.** The byte and object counts are enforced natively. The memory figure
  that came with them has been restated: sessions now run `GOGC=50`, which
  measures 3.27 MB peak RSS per MiB rather than 4.59, so a 16 MiB session implies
  roughly 65 MB including fixed process RSS, not 85 MB. See section 11 and
  `docs/benchmarks/session-baseline.md`.
- [ ] Which capabilities must be present for an agent adapter?
- [ ] How are child-agent credentials handed off?
- [ ] How are cleanup errors reported without hiding the primary error?
- [ ] Which run-through behaviors are safe enough for the first release?
- [ ] Which external pilots define success?
