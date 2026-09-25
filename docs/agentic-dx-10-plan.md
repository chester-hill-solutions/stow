# Stow 10/10 Plan

## 1. Product target

**Primary job:** Give any coding agent an isolated S3 bucket for one task in under 60 seconds, with one command, no credentials, no network, and guaranteed cleanup.

### Non-goals

- Production object storage.
- Hosted SaaS or dashboard.
- Full AWS S3 parity.
- Multiple storage engines.
- Default telemetry.
- Shared multi-tenant administration.

## 2. Delivery sequence

| Phase | Outcome | Release | Depends on |
|---|---|---|---|
| 0. Baseline | Reproducible green build and regression tests | — | — |
| 1. Safety | No accidental cloud writes, deletion, or orphaned processes | `0.2.0` | Phase 0 |
| 2. Agent interface | CLI and MCP complete the core job without an SDK | `0.3.0` | Phase 1 |
| 3. Storage v3 | Streaming, indexed metadata, migrations | `0.4.0` | Phase 1 |
| 4. Conformance | One contract passes every interface | `0.5.0` | Phases 2–3 |
| 5. Distribution | Clean-room installs on Linux, macOS, Windows | `1.0.0-rc` | Phase 4 |
| 6. Trust and feedback | Security process, diagnostics, funding | `1.0.0` | Phase 5 |

Estimate assumes two experienced engineers.

---

## Phase 0 — Baseline

### Tasks

1. Run and record:
   - `make test-all`
   - `make standards`
   - Node tests on Node 20, 22, and 24
2. Add failing regression tests for every known Phase 1 defect.
3. Extract shared startup logic into `internal/serverapp`.
4. Define versioned schemas:
   - Readiness protocol
   - Session JSON
   - Doctor JSON
   - Structured errors
   - MCP tool results
5. Add performance baselines for startup, listing, and parallel sessions.

### Exit criteria

- Clean checkout passes every gate.
- Each known defect has a failing test.
- CLI, SDK, and future MCP adapters share one startup path.

---

## Phase 1 — Safety and lifecycle

### 1. Local mode by default

- `Stow.start()` passes `--mode local` unless the caller explicitly requests run-through.
- `mirrorWrites` never enables live writes implicitly.
- Run-through requires explicit mode plus explicit live-write consent.
- Non-loopback and non-HTTPS upstreams fail unless an explicit insecure flag is set.
- Add tests proving local mode makes zero upstream requests.

**Status: the two data-loss items are done; the mode default is deferred by decision.**

- **Landed.** `mirrorWrites` no longer grants live-write consent on its own.
  `STOW_ALLOW_LIVE_WRITES=true` or `--allow-live-writes` is required and is
  sufficient under either policy, and an explicit `false` is a refusal that the
  policy cannot override. Recorded in `docs/adr/0005-live-write-requires-explicit-consent.md`.
- **Landed.** Local mode is now proven to make zero upstream requests rather than
  assumed to. `buildStore` is the single function that constructs a store and it
  never constructs an upstream client in local mode, so a local server holds no
  object capable of reaching a provider. `TestLocalModeMakesZeroUpstreamRequests`
  sets the full hazardous environment, forces local mode, exercises
  create/put/get/head/list/delete against a recording upstream, and requires zero
  hits. The consent flag and the store wiring are asserted separately, because
  either alone is insufficient.
- **Deferred.** The mode default itself is unchanged: a developer with upstream
  credentials in their environment still gets run-through, per ADR 0001. Flipping
  it reverses that ADR and breaks every existing run-through user, which is a
  1.0 breaking-change proposal rather than a side effect of a safety fix. The
  reasoning is recorded in ADR 0005 under "Why not change the mode default".
- **Open.** The insecure-flag item is untouched: `NewS3Client` still takes a
  configured endpoint verbatim with no loopback or HTTPS check.

### 2. Cross-platform process ownership

- Linux: retain `PR_SET_PDEATHSIG`.
- macOS: retain the kqueue descriptor and service events in a goroutine.
- Windows: use a Job Object.
- Add real macOS and Windows lifecycle CI.

### 3. Reliable startup

- Buffer readiness data until a complete newline-delimited record arrives.
- Catch parsing errors and reject startup.
- Handle descriptor `error` and `end` events.
- Honor public timeout and abort options.
- Use the region returned by the server instead of forcing `us-east-1`.

### 4. Safe reset

- Remove unrestricted `cleanSlate`.
- Add `resetOwnedData()`.
- Require a Stow ownership marker.
- Refuse root, home, and workspace paths.
- Require recursive deletion to be explicit.

**Status: done, with one naming deviation.**

All five items are landed and recorded in
`docs/adr/0006-owned-data-directory-reset.md`. The filesystem store writes a
`.stow-owner` marker when it finishes initializing a data directory, and a reset
requires that marker. Root, home, and the working directory are refused, and so
is any *ancestor* of them, which is what closes the `dataDir: ".."` bypass that
an equality check would miss. The check compares resolved paths and is
ancestor-or-self rather than descendant, so the documented `.stow` default still
works.

The deviation: `cleanSlate` was not removed but kept as a deprecated alias for
`resetOwnedData`, carrying identical checks. The plan's own compatibility rule is
additive migration, and the alias no longer deletes anything the new name would
not. Both spellings are equally safe; only the new one leads the docs and types.

One consequence to be aware of when upgrading: a data directory created before
this change has no marker, so the first reset is refused. Nothing is deleted and
no data is lost; the operator removes it once by hand.

The Go writer of the marker and the TypeScript reader are held in step by
`scripts/check-version.mjs`, because a delete decision that depends on a
duplicated name will eventually depend on the wrong one.

### 5. Admin security

- Require a separate admin token.
- Disable admin routes when the token is absent.
- Replace reflected CORS with an explicit allowlist.
- Keep destructive outbox tools out of agent-facing interfaces.

### Exit criteria

- No ambient environment can cause an upstream write.
- Sessions leave no processes on any supported OS.
- Malformed readiness input rejects instead of hanging.
- No API can delete an unowned directory.
- Admin operations require credentials.

---

## Phase 2 — Agent-native interface

### 1. Language-agnostic session CLI

```bash
stow session start --json
```

Output:

```json
{
  "protocolVersion": 1,
  "endpoint": "http://127.0.0.1:41234",
  "region": "us-east-1",
  "accessKeyId": "...",
  "secretAccessKey": "...",
  "bucket": "stow-session-...",
  "pid": 1234,
  "capabilities": {}
}
```

The command must:

- Start one isolated session.
- Create its bucket.
- Print one JSON object.
- Wait for SIGINT or SIGTERM.
- Clean up owned data.
- Support a caller-supplied data directory without deleting it.

### 2. MCP stdio server

Implement `stow mcp` as a thin adapter over the existing runtime.

Initial tools:

- `create_bucket`
- `delete_bucket`
- `list_buckets`
- `put_object`
- `get_object`
- `head_object`
- `list_objects`
- `copy_object`
- `delete_object`
- `usage`

Do not expose health, metrics, outbox retry, or outbox discard by default.

### 3. Structured errors

Every interface must return:

```json
{
  "error": {
    "code": "precondition_failed",
    "message": "...",
    "retryable": false,
    "resource": "bucket/key",
    "requestId": "..."
  }
}
```

### 4. Fixture workflow

```bash
stow fixture apply fixtures.json
stow fixture export --json
stow fixture reset
stow fixture diff expected.json
```

### Exit criteria

- A Python, Go, or shell agent can use Stow through the CLI alone.
- An agent can complete the core object job through MCP alone.
- Neither path requires application-specific integration code.

---

## Phase 3 — Storage format v3

### New layout

```text
<hash>.blob
<hash>.meta.json
```

### Tasks

1. Hash filenames instead of hex-encoding full keys.
2. Store the original key in metadata.
3. Stream object bodies to disk.
4. Read metadata only while listing.
5. Support keys up to the full S3 limit.
6. Preserve checksums, content type, and metadata.
7. Add streaming copy.
8. Add format-version detection.

### Migrations

- Read the current format without modification.
- Write the new format.
- Convert existing data on explicit migration.
- Support `stow migrate --check` and `--apply`.
- Back up before conversion.
- Add interrupted-migration recovery.

### Exit criteria

- A 1,024-byte key works on every filesystem.
- Listing does not load object bodies.
- Streaming a large object does not buffer it fully.
- Migration and restore pass crash-recovery tests.

---

## Phase 4 — S3 correctness and conformance

### Fixes

1. Multipart list-parts pagination.
2. Multipart delimiter handling.
3. Multipart metadata and checksum preservation.
4. Conditional GET returns `304`.
5. Empty-object range requests return `416`.
6. Copy-source URL decoding.
7. Copy metadata-size limits.
8. Run-through policy errors map to 4xx rather than 500.
9. Bucket deletion purges cache.
10. Multipart completion invalidates cache.
11. Copy and cache operations respect cache quotas.
12. Retry-worker failures reach logs and health state.

### Conformance

Run one shared corpus against:

- Memory backend
- Filesystem backend
- Runtime adapter
- Node SDK
- MCP server
- Embedded runtime

Add:

- Malformed XML fuzzing
- Auth-header fuzzing
- Checksum fuzzing
- Range-request property tests
- Live S3 and R2 differential tests

### Exit criteria

- Every surface passes the same corpus.
- No known P1 correctness defects remain.
- Live-provider tests cover read-through, cache fallback, delete, list, copy, multipart, and retry.

---

## Phase 5 — Distribution and trust

### Release engineering

- Include `LICENSE` in every npm package.
- Add a clean-room install test for packed artifacts.
- Verify platform-package resolution and integrity.
- Add `stow version`.
- Publish SHA-256 checksums and provenance.
- Tag and publish `0.2.0` as a safety release before storage v3.

### Platforms

- Linux x64 and arm64
- macOS x64 and arm64
- Windows x64

Run full runtime tests on each supported platform family.

### Security

Add:

- `SECURITY.md`
- `CONTRIBUTING.md`
- `CODE_OF_CONDUCT.md`
- Dependency update automation
- `govulncheck`
- npm or OSV scanning
- Real browser IndexedDB tests

### Exit criteria

- A packed artifact installs into an empty project and passes one object round trip.
- Every supported platform runs the core test suite.
- No known critical dependency or security findings remain open.

---

## Phase 6 — Feedback and sustainability

1. Add `stow doctor --bundle` with automatic secret redaction.
2. Add opt-in diagnostics; no default telemetry.
3. Dogfood sessions in at least three coding-agent harnesses.
4. Publish machine-readable compatibility fixtures.
5. Add GitHub Sponsors or equivalent funding.
6. Offer paid support while keeping the core FOSS.

---

## Release gates

### `0.2.0`

- Local-only default
- No implicit live writes
- macOS lifecycle fixed
- Readiness parsing fixed
- Safe reset
- Authenticated admin routes

### `0.3.0`

- `stow session start --json`
- `stow mcp`
- Stable error schema
- Fixture commands

### `0.4.0`

- Storage format v3
- Streaming I/O
- Migration and restore

### `0.5.0`

- Shared conformance across all interfaces
- Fuzzing
- Live-provider coverage

### `1.0.0`

- Clean-room installs
- Linux, macOS, Windows runtime CI
- Security process
- No open P0 or P1 defects

## 10/10 acceptance criteria

- Install to first object in under 60 seconds.
- Local mode produces zero outbound requests.
- 1,000 parallel sessions have unique credentials, buckets, and endpoints.
- No orphaned processes on supported platforms.
- Every valid S3 key works on the filesystem backend.
- One conformance corpus passes every interface.
- MCP completes the core job without SDK code.
- Packed artifacts install cleanly.
- Storage upgrades and restores are tested.
- No open critical security findings.
