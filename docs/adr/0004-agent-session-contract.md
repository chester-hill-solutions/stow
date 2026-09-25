---
status: accepted
---

# Agent session contract

This ADR records the contract that the TypeScript and Python session APIs
implement. It exists so that the two language packages cannot drift apart while
the agent-facing surface is built, and so that the decisions made in
`docs/agent-dx-plan.md` phase 0 are written down rather than re-argued per
implementation.

Measurements referenced here are from `docs/benchmarks/session-baseline.md`.
Distribution measurements are from `docs/distribution-spike.md`.

## Context

Stow is an S3-compatible server with a process-owned TypeScript client, a direct
embedded runtime, and a WASM profile. The agent direction adds a higher-level
promise: a disposable S3-shaped workspace for a short-lived execution context,
with no lifecycle glue in user code.

The risk is not building it. The risk is building it twice, differently, in two
languages, and discovering the divergence after both are published.

## 1. The default promise

A default session is:

- **local only**, never inheriting `STOW_*`, `S3_*`, or `AWS_*` configuration;
- **memory backed**, with no data directory unless one is explicitly requested;
- bound to a **generated bucket** the session creates before user code runs;
- reachable through a **loopback S3 endpoint** using generated credentials;
- **disposed** when the scope exits, on success, failure, or cancellation.

Advanced profiles (filesystem persistence, run-through, embedded, external
endpoint) stay explicit. A default session never silently inherits one.

## 2. Ownership

| Resource | Owner | Released by |
|---|---|---|
| Child process | the session | `close()` |
| Temporary directory | the session, only if the session created it | `close()` |
| Credentials | the session, generated per session | discarded with the process |
| S3 client | the caller, from the session's config | caller's `destroy()` |

The session **borrows** the WASM host and the S3 SDK client. It never closes
either. `close()` is terminal and idempotent: it drains already-accepted
operations, rejects later ones with a `closed` error, and returns the same
result on repeated calls.

Cleanup never removes a caller-owned directory.

## 3. Default limits

From the measured profile, not from assumption:

| Limit | Value | Reason |
|---|---|---|
| Session bytes | 16 MiB | ~85 MB peak RSS at the measured 4.59 MB-per-MiB multiplier |
| Session objects | 1,000 | Far above an agent workload; negligible cost |
| Single request body | 8 MiB | Bounds one PutObject; enforced at the HTTP boundary |
| Startup deadline | 30 s | Existing `STARTUP_TIMEOUT_MS` |
| Shutdown deadline | bounded | Force-kill after the deadline |

These are defaults a caller may raise or lower. The server's own
`--max-bytes` and `--max-objects` flags remain unbounded by default so that an
existing long-lived `stow serve` does not change behavior; a session passes
explicit values.

## 4. Ready protocol

`STOW_READY` text on stdout remains supported for existing consumers. A
versioned JSON channel is added behind `--ready-fd`, writing exactly one object
to that descriptor with logs on stderr.

```json
{
  "protocolVersion": 1,
  "binaryVersion": "0.2.0",
  "endpoint": "http://127.0.0.1:43127",
  "region": "us-east-1",
  "accessKeyId": "...",
  "secretAccessKey": "...",
  "mode": "local",
  "backend": "memory",
  "capabilities": {
    "persistent": false,
    "multipart": true,
    "upstream": false,
    "conditionalWrites": true,
    "presignedUrls": true,
    "maxBytes": 16777216,
    "maxObjects": 1000,
    "maxRequestBytes": 8388608
  }
}
```

Rules:

- credentials appear in this message and **not** in ordinary logs;
- a protocol or binary version mismatch fails with a specific error rather than
  a parse failure;
- no client parses human-formatted log output.

Moving credentials off stdout is a security improvement that must land **before**
the Python client, so both languages consume the same channel.

## 5. Error model

Session lifecycle failures use a structured error with a stable `code`. S3 errors
from the SDK pass through unchanged; the session adds context only for failures
in acquiring or releasing the session.

Codes: `startup`, `closed`, `cancelled`, `quota_exceeded`, `invalid_options`,
`capability_mismatch`, `binary_not_found`, `protocol_mismatch`, `backend_error`,
`internal`.

`binary_not_found` already exists as `StowBinaryNotFoundError` in the TypeScript
package and names the resolution order. The Python package must use the same
code for the same condition.

## 6. Binary distribution

The native binary ships in platform optional packages
(`@chs/stow-s3-<os>-<arch>`) resolved by the package before `PATH`, so a plain
install starts a session with no environment setup. The resolution order is:
bundled platform package, `STOW_BIN`, monorepo `bin/stow-s3`, `stow` on `PATH`.

The release packs each platform package from artifacts the same run built, and
`make check-version` verifies every platform package against the single Go
version source, so a binary cannot be published from a different release than
the JavaScript that resolves it.

PyPI distribution is `stow-s3`, import package `stow_s3`, Python 3.10 or newer.
`stow` is taken on PyPI by an unrelated package.

## 7. Deferred for the first release

Recorded as explicit non-goals, not oversights:

- **Multipart staging and request-concurrency limits.** Each request is already
  bounded at 8 MiB and the session at 16 MiB, so the remaining exposure is
  unbounded request *count*. Adding a limit is straightforward once concurrent
  session behavior is measured, and a speculative semaphore now would be
  untested policy.
- **Async Python.** Sync lifecycle first; async must prove client and process
  cleanup before it is public.
- **A shared daemon or child pool.** Blocked on measurement, behind the session
  seam.
- **Streaming writes.** The measured 4.59x memory multiplier is a known and
  measured cost of the v1 buffered path, and reducing it is phase 2 work. It is
  not hidden, and the quota defaults are sized against it.
- **Embedded S3 wire compatibility.** The native path has an in-process adapter;
  the public `EmbeddedStow` and `@chs/stow-s3/browser` profiles stay direct object
  interfaces with no S3 surface.

## 8. Consequences

- The two language packages are testable against one protocol and one corpus.
- A session's memory cost is predictable enough to size quotas, and the sizing
  is recorded with the measurement that produced it.
- The version skew that a platform package could introduce is caught by a gate
  rather than by a user.
- Anything a caller must clean up by hand is a bug in this contract, not a
  documented step.
