# The Stow Environment Primitive

**Status:** proposed
**Date:** 2026-09-26
**Scope:** the abstraction every Stow surface is composed from
**Relationship:** architectural direction for the whole repository. Where this
document and an ADR both speak to a decision, **the ADR is normative and this
document points at it** — see section 0.3. This document does not restate those
decisions, because a second copy of a decision is a second thing that can drift.
**Relationship:** additive to `docs/agent-dx-plan.md`, which owns the delivery
order. Section 20 reconciles the two, because they were written at different
times and initially disagreed about what to do next.

---

## 0. Before reading further

### 0.1 What this document is

A direction, not a rewrite. Stow already contains most of this architecture in
the shape of working code. The claim is that the *shape* is right and is not
written down, and that a codebase whose architecture is only legible from its
prose will eventually disagree with it.

The two sentences that matter:

> Do not make Stow smaller. Make its abstraction smaller.

The implementation can support many environments, runtimes, stores, policies and
interfaces while having one coherent primitive underneath them. Everything above
the store is composable. Everything below it is conformant.

### 0.2 What was verified, and what was not

The original draft of this document opened with a list of fourteen things Stow
"currently provides." That list is the kind of claim this repository has been
burned by repeatedly, so it was measured rather than asserted. Full results in
[section 2](#2-what-stow-actually-provides-today). Two of the fourteen are
false, and they are the two that matter most:

- **Read-through does not read through.** A bucket that exists only upstream
  returns `bucket not found` with the upstream call count at zero. Measured, in
  both wirings, on 2026-09-26.
- **Upstream write propagation does not propagate.** A mirror-writes write never
  reaches the outbox.

Neither is a missing feature. Both are present, wired, and covered by passing
tests. They are the standing example of why the claims in this document are
marked with evidence, and [section 21.2](#212-run-through-measured-and-worse-than-untested)
records what to do about it.

Everything else in that list held up. Section 2 is a table with a verdict and a
reference for each row, so a future reader can re-run the check rather than trust
the table.

### 0.2.1 This document was wrong five times, and the corrections are kept

Measuring the *draft's* claims is not the same as verifying *this document's*.
The first version of this file was merged having got five things wrong, all of
them caught by the Phase 2 audit in [section 21.3](#213-coupling-audit-what-phase-2-found)
rather than by review. They are recorded in place rather than quietly fixed,
because "verify it" is not a property a document can claim about itself:

| This document claimed | Actually |
|---|---|
| Python client does an unconditional `s3.create_bucket` (`__init__.py:13`) | That line is inside a **module docstring** — a usage example. The client contains no S3 call. Found by grepping for a string and reporting the hit without reading it |
| WASM is a store (§1, §13, §24) | There are three Go stores — **memory, filesystem, workspace**. WASM is the *host*; `cmd/stow-wasm` calls `pkg/stow.Open`, which is the memory store |
| The store/S3 coupling "is a naming problem" (§3.2) | `CompositeETag` is Amazon's multipart ETag algorithm and `PaginateMultipartUploads` is S3's marker algorithm, both in `internal/storage`. It is S3 *behaviour* |
| `internal/storage` has no contract test (§13, §20) | It has `backend_contract_test.go`: 9 behaviours across all three stores. The real gap is that it has no *checksum* case — exactly where a real divergence ships |
| "A filesystem-backed environment changes one line" (§12) | `pkg/stow.Options` has no `Store` field and `runtime/types.go:72` refuses non-memory, so a second constructor exists |

Two of those five are the same mistake this document was written to prevent: a
grep hit and a plausible inference, both reported as findings. The fifth is the
cost of writing an API direction for a system that has not been refactored yet —
`Store: stow.Memory(...)` is a target, and the draft described it as current.

**The rule, stated once so it can be applied:** a line number is not a claim, and
neither is a method name. Read the line, run the code, or mark it unverified.


### 0.3 Where the decisions already live

This document does **not** decide these. It points at them.

| Topic | Decided by |
|---|---|
| Local authority, and policy-is-not-consent (§3.7, invariant 5) | [ADR 0002](../adr/0002-sdk-compatibility-and-mirror-writes.md), narrowed by [ADR 0005](../adr/0005-live-write-requires-explicit-consent.md) |
| Lifetime, and the workspace's exception to it (§3.4) | [ADR 0009](../adr/0009-workspace-outlives-process.md) |
| Workspace as the default (§6) | [ADR 0007](../adr/0007-workspace-is-the-default.md), [ADR 0008](../adr/0008-workspace-backend-real-files.md) |
| Sessions as capability issuers (§5) | [ADR 0004](../adr/0004-agent-session-contract.md) |
| The embedded runtime (§1) | [ADR 0003](../adr/0003-embedded-runtime.md) |
| The S3 compatibility surface (§8) | [`docs/compat-contract.md`](../compat-contract.md) |
| Cross-runtime agreement (§9) | [`docs/workspace-contract.md`](../workspace-contract.md) §8 |

Read the ADR when the question is *what was decided and why*. Read this document
when the question is *what the pieces are and how they compose*.

---

## 1. The primitive

Stow is not an S3 emulator. It is a portable, policy-controlled object-storage
capability that can be instantiated, scoped, composed, embedded, persisted,
exposed through standard interfaces, and optionally related to an upstream
object store.

S3 is an important interface to Stow. It is not Stow itself.

```
Stow Environment
│
├── Namespace      what is visible
├── Store          where it lives
├── Interfaces     how you reach it
├── Lifetime       who owns it, and when it dies
├── ResourcePolicy how much it may cost
├── AccessPolicy   who may touch it
├── UpstreamPolicy what it is related to
└── Capabilities   what it can do
```

Stow is a capability granted to a piece of software. S3 is what makes that
capability immediately useful to software that already exists, which is why it
stays.

Different products should fall out of composing these dimensions, not out of
proliferating Stow implementations:

| | Store | Interfaces | Lifetime | Quotas | Upstream |
|---|---|---|---|---|---|
| Ephemeral test store | memory | S3 | scope | strict | none |
| Agent workspace | filesystem | filesystem + S3 | task | strict | none |
| Persistent local dev | filesystem | S3 | explicit | host-set | none |
| Embedded application | memory | native | application | host-set | none |
| Browser application | memory (in a WASM host) | native | explicit | host-set | none |
| Read-through dev env | filesystem | S3 | explicit | host-set | read-through |
| Mirrored dev env | filesystem | S3 | explicit | host-set | write-through + live authority |

The engineering objective is not to reduce Stow's flexibility. It is to make
that flexibility systematic.

---

## 2. What Stow actually provides today

Verified 2026-09-26 against the tree at `04470e4`. This table replaces a prose
list, because a prose list cannot be re-checked.

| Capability | Verdict | Evidence |
|---|---|---|
| S3 HTTP server | holds | `internal/s3api`, 55.9% covered |
| Memory + filesystem operation | holds | `internal/storage/memory.go`, `internal/storage/fs` |
| Scoped TS + Python sessions | holds | `packages/stow-s3/src/session.ts`, `packages/stow-s3-py` |
| Embedded Go runtime | holds | `pkg/stow`, [ADR 0003](../adr/0003-embedded-runtime.md) |
| WebAssembly runtime | holds | `wasm/`, cross-compiled in CI |
| Browser persistence | holds | `packages/stow-s3/src/indexeddb-store.ts` |
| Resource limits | holds, with a gap | `MaxBytes`/`MaxObjects` are host-settable; the S3 request-body cap is **not** — see below |
| Process-bound lifecycle | holds on 2 of 3 platforms | `internal/parentwatch` has `darwin`, `linux`, `unsupported`. No Windows implementation (W12) |
| Filesystem persistence | holds | `internal/storage/workspace` |
| **Read-through** | **does not work** | measured: upstream-only bucket → `bucket not found`, 0 upstream calls |
| **Upstream write propagation** | **does not work** | measured: write never reaches the outbox |
| Capability negotiation | holds | `internal/ready`, `protocolVersion` present |
| Diagnostics | holds | `cmd/stow-s3/doctor.go` |
| Workspace functionality | holds | W0–W4, [ADR 0007](../adr/0007-workspace-is-the-default.md) |

Two gaps worth naming precisely, because both are cases where the environment's
*advertised* limits and its *enforced* limits can disagree — which is the exact
failure [section 3.5](#35-resource-policy) exists to prevent:

1. `MaxRequestBytes` is `DefaultMaxRequestBytes`, a constant of 8 MiB, and
   `cmd/stow-s3` never sets `Config.MaxRequestBytes`. There is no flag. A PUT
   above 8 MiB fails with `EntityTooLarge` no matter what the host configures,
   while the readiness payload advertises a `maxBytes` the host did choose. This
   is W6, and it is a §3.5 violation that is already shipping.
2. The §3.4 parent-death guarantee is true on Linux and macOS and silently
   absent on Windows. A caller cannot tell the difference from the readiness
   payload. This is W12.

---

## 3. The eight dimensions

`S = (N, B, I, L, Q, A, U, C)` — namespace, store, interfaces, lifetime,
resource policy, access policy, upstream policy, capabilities.

These must stay as independent as practical. A configuration should describe
*what environment should exist*, not select a bespoke implementation of a
particular use case.

### 3.1 Namespace

The logical universe of buckets and objects visible to an environment.

It must not inherently imply memory, filesystem, S3, HTTP, process lifetime, or
upstream storage. Those are separate dimensions. The namespace owns object
semantics; interfaces translate external operations into namespace operations;
stores decide how namespace state is represented and retained.

### 3.2 Store

The backing store answers one question: *where does the namespace's state live?*

The contract is `internal/storage.Store` (`internal/storage/store.go:9`). Two
honest observations about it, because the primitive document should describe the
real one:

**The mechanism is clean, and it should be said plainly.** `internal/storage`
imports nothing from the rest of the project — verified by grep, not by
inspection. No store file contains an HTTP status code, an XML type, an AWS SDK
type, SigV4, or `smithy`. `internal/s3api/errors.go:106` (`mapStorageError`) is a
clean, single, upward-only translation table. This is the strongest thing in the
codebase and it is the reason the layering is worth preserving.

**The behaviour is S3-shaped, and this is real coupling — not naming.** The first
draft of this document said the S3-shaped vocabulary "is a naming problem". That
was wrong, and the audit in [section 21.3](#213-coupling-audit-what-phase-2-found)
found the algorithms. In `internal/storage`:

| What | Where | Why it is S3, not object model |
|---|---|---|
| `CompositeETag` | `util.go:87` | Amazon's documented multipart ETag: md5 of concatenated part digests, `-N` suffix. Every store calls it, so the store **is** the S3 ETag format |
| Quoted-hex ETag | `util.go:78` | `ObjectMeta.ETag` embeds HTTP entity-tag quoting; `preconditions.go:31` has to `strings.Trim(…, "\"")` to compare |
| `ComputeChecksum` | `checksum.go:14` | Big-endian CRC32 + base64 is the S3 wire encoding, chosen in the store and reused by `s3api` |
| `PaginateObjects` | `util.go:256` | S3's `ListObjectsV2` rule, including `CommonPrefixes` counting against `MaxKeys` — a different protocol would get different truncation from the same store |
| `PaginateMultipartUploads` | `util.go:205` | S3's exact marker algorithm: sort by `(Key, Initiated, UploadID)`, derive `NextKeyMarker`/`NextUploadIDMarker` |
| `matchesETagHeader` | `preconditions.go:18` | Parses `w/` weak prefixes, comma-separated lists, and `*` — that is RFC 9110 `If-Match` grammar, in the store |
| `reservedBucketPrefixes` | `util.go:16` | `xn--`, `sthree-`, `amzn-s3-demo-`, `amzn_s3_demo_` — AWS virtual-hosted-bucket reserved prefixes |
| S3 service limits | `util.go:51,103,154` | 1024-byte key cap, 10000-part cap, 1000 `MaxKeys` default |

So the honest statement of invariant 3 is: **it holds for imports and control
flow, and it is violated for behaviour.** The store layer implements S3's
object-metadata wire representations, largely because that is where the bytes are
hashed, and it enforces S3's service limits.

That is a defensible place for some of it — an MD5 of the bytes has to be
computed somewhere, and computing it in the store is the obvious place. It is
not defensible as a permanent position, because it means a second interface gets
S3's limits and S3's ETag format whether it wants them or not. The draft's claim
that a store "does not know which protocol a request arrived on" is true of the
call path and false of the contract.

**The interface is also S3-shaped in its method names.** `ListObjectsV2` is the
S3 API name, not a generic one, plus `CopyObject`, `DeleteObjects`, and **eight**
multipart methods. Multipart is arguably a legitimate object-model concept
(streaming an object larger than memory) that S3 also happens to expose.
`ListObjectsV2` is not. The draft proposed a `Reset()` method; no such method
exists, and the draft's interface omitted all eight multipart methods — which is
how a document ends up describing a system that was never built.

**One dead protocol surface.** `ListPartsPage` is implemented by all three stores
and forwarded by `internal/runtime/adapter.go:216`, but it is **not** in
`storage.Store`, and `internal/s3api/handlers_multipart.go:194` never calls it —
it calls `ListParts` and hardcodes `MaxParts: 1000`. Meanwhile
`internal/runtime/multipart.go:104` discovers it by type assertion. Since
`runthrough.Adapter` does not implement it, run-through mode silently falls back
to fetching every part and paginating in memory, unbounded, for a 10,000-part
upload. The store contract test asserts a property production does not have.


### 3.3 Interfaces

An interface answers: *how can something interact with this namespace?*

```
                  Namespace
                      │
         ┌────────────┼────────────┐
         │            │            │
      S3 HTTP      Native API   Filesystem
```

S3 is an adapter:

```
S3 request → authentication → S3 semantic translation → primitive operation → Store
```

not:

```
Stow = S3 server
```

This is what lets S3 compatibility evolve without pushing AWS-specific concepts
down into the storage model. [`docs/compat-contract.md`](../compat-contract.md)
is the S3 surface; this document is the boundary it sits on.

### 3.4 Lifetime

Lifetime answers: *who owns this environment, when does it cease to exist, what
happens if the owner disappears, is state destroyed on close, can it be
reopened?*

`withStow()` is a scope lifetime — guaranteed closed after the callback.
`stow serve` is an explicit lifetime — running until stopped. Parent-death
monitoring belongs here and is not an incidental server feature.

**One correction to the original draft.** It enumerated `ScopeLifetime`,
`ProcessLifetime`, `ApplicationLifetime`, `PersistentLifetime`, `ExplicitLifetime`
— and separately, in its own profile table, gave the agent workspace a *task*
lifetime. Those two lists do not agree, and the disagreement is not cosmetic:
[ADR 0009](../adr/0009-workspace-outlives-process.md) exists precisely because a
workspace is **neither** scope- nor process-bound. It outlives the process that
created it, because an agent process is crash-prone, preempted, and resumed, and
treating its scratch directory as a child process's property loses the work
exactly when it was most expensive to produce.

So the enumeration is missing its hardest case, and the profile table has it. The
reconciled list:

| Lifetime | Owner | State on close | Reopenable |
|---|---|---|---|
| Scope | the callback | destroyed | no |
| Process | the process | destroyed | no |
| **Task** | **a task, which may span processes** | **retained** | **yes** |
| Application | the host process | destroyed | no |
| Explicit | an operator | retained | yes |

### 3.5 Resource policy

Max bytes, max objects, max object size, max request size, multipart state, and
eventually bandwidth and operation count.

The invariant:

> Limits are enforced by the environment, not merely requested by the client.

This matters most for agents and semi-trusted workloads. A caller should be able
to issue "S3-compatible storage, up to 64 MiB, for the lifetime of this task"
without trusting the consumer to respect the boundary. Section 2 records two
places where the invariant is already violated.

### 3.6 Access policy

Access and authentication stay separate from storage. The environments in play:
same-process embedded, locally authenticated, private network, capability
credential, administratively controlled.

SigV4 is an **S3-interface** authentication mechanism. It does not define Stow's
internal authorization model. Admin credentials, S3 credentials, and upstream
credentials are three separate security domains and must stay three.

The existing principle that **loopback is not itself a privilege boundary**
stands and is carried into [section 29](#18-security-model).

### 3.7 Upstream policy

This is the dimension most likely to accidentally destroy composability, because
it is the one with a shortcut available.

An upstream is **not a backing store**. It is a *relationship* between a Stow
namespace and another object-storage namespace.

```
Local Namespace
      │
      │ upstream policy
      ▼
External Namespace
```

Policies: none, read-through, write-through, mirror, promote, snapshot.

**Prefer this shape:**

```
Store + UpstreamPolicy
```

**Over this shape:**

```
ReadThroughFilesystemStore
MirrorFilesystemStore
CachedS3FilesystemStore
```

This is not a style preference. The second shape is what the current
`internal/runthrough` subsystem drifted toward: 3,646 lines — 23% of all
production Go — at 66.3% statement coverage, with 75 passing tests and a read
path that has never once reached an upstream. The causes are recorded in
[section 21.2](#212-run-through-measured-and-worse-than-untested), and they are
what happens when a policy and a store become one thing.

### 3.8 Capabilities

Every environment should be self-describing, so consumers inspect rather than
infer from version, backend, runtime, operating system, or launch method.

The versioned readiness channel already does most of this.
`internal/ready/ready.go` advertises `protocolVersion`, `backend`,
`persistent`, `multipart`, `upstream`, `conditionalWrites`, `presignedUrls`,
`maxBytes`, `maxObjects`, `maxRequestBytes`, and `mode`.

The concrete deltas to the shape proposed in the original draft:

| Proposed | Reality | Change |
|---|---|---|
| `"interfaces": ["s3"]` | absent | add; this is what makes §1 composable from the outside |
| `"limits": { maxBytes, maxObjects }` | flat, beside `capabilities` | nest, so limits are one object |
| `"capabilities": ["multipart", …]` | named booleans | either form works; pick one and version it |
| `rangeReads`, `checksums` | not advertised at all | add — they are implemented but invisible |
| `mode` | present | replace with profile (§4) |
| `maxRequestBytes` | present | keep; it is the one limit that is currently unraisable |

The test a consumer should be able to pass: *what storage capability did I
receive?* — without knowing how it was instantiated.

---

## 4. Profiles

A profile is a named composition of primitive options, and **nothing else**.

| Profile | Store | Interfaces | Lifetime | Persistence | Limits | Upstream |
|---|---|---|---|---|---|---|
| Ephemeral | memory | S3 | scope | none | bounded | none |
| Workspace | filesystem | filesystem + S3 | task | yes | bounded | optional |
| Persistent local | filesystem | S3 | explicit | yes | host-set | none |
| Read-through | filesystem | S3 | explicit | yes | host-set | read-through |

Profiles expand into ordinary configuration. The failure mode to avoid:

```go
if profile == Agent { ... }
if profile == Browser { ... }
```

in favour of:

```
Profile → Configuration → same primitive
```

**One mode-conditional must survive this refactor.** `cmd/stow-s3/main.go:171`
refuses run-through live writes on the memory backend:

```go
if mode == runthrough.ModeRunThrough && backend == "memory" && runthrough.PropagatesUpstream(config) {
    return fmt.Errorf("run-through live writes require the filesystem backend")
}
```

That reads like exactly the anti-pattern above, and it is not. It encodes a real
invariant: upstream propagation requires a durable local copy, because local is
authoritative, and memory cannot be one. A profile refactor that flattens this
into configuration would remove a safety property while looking like a
simplification. The general rule: a conditional that guards a safety invariant
is not a profile fork, however much it looks like one.

Current state: modes are a two-value enum, `local` and `run-through`
(`internal/runthrough/config.go:15`). Profiles do not exist yet.

---

## 5. Sessions are capability issuers

A session is not a helper that starts the S3 server. It issues a temporary
storage capability.

```ts
await withStow(async (stow) => {
    // this scope possesses a storage capability
});
```

That capability currently carries: endpoint, credentials, bucket, limits,
capabilities, lifetime.

This is the model that makes the multi-agent story work — an orchestrator
issuing independent capabilities to independent agents, each with its own
namespace, quotas, lifetime and policies, without any agent knowing what Stow
is. [ADR 0004](../adr/0004-agent-session-contract.md) is normative for the contract
as it stands.

---

## 6. Workspace semantics

Workspace mode is strategically important because it is the case where multiple
interfaces address one namespace:

```
                   same state
                      │
            ┌─────────┴─────────┐
            │                   │
      filesystem tools        S3 SDK
            │                   │
            └─────────┬─────────┘
                      │
                  namespace
```

Do not implement it as filesystem state synchronized with S3-specific state.
That creates two sources of truth. There is one authoritative representation,
and it is the namespace. [ADR 0008](../adr/0008-workspace-backend-real-files.md)
decides that the namespace *is* the filesystem, which is the strongest available
form of this invariant.

---

## 7. WASM

WASM is retained as a runtime implementation of the same primitive, not as a
second Stow with subtly different semantics.

The test: **can one conformance model validate native, WASM, memory, and
persistent implementations wherever their advertised capabilities overlap?**

This repository already answers yes — `conformance/` runs a shared corpus
across `STOW_CONFORMANCE_BACKEND` of `memory`, `filesystem`, and `runtime`, and
the runtime backend is itself exercised over both memory and filesystem
(`make test-conformance`). If the answer ever becomes no, the abstraction has
diverged and that is the finding.

---

## 8. S3 compatibility

Stow does not need to implement all of Amazon S3. It needs to be exceptionally
reliable about the subset it claims, and it needs to be honest about where the
subset ends.

[`docs/compat-contract.md`](../compat-contract.md) is the authoritative
capability matrix — supported operations, URL styles, authentication, CORS,
presigned URLs, and a conformance suite in its section 2. This document does not
restate it.

The principle it should add: for every operation `O` in the advertised surface,
and for every semantic dimension claimed — request shape, response shape, errors,
headers, conditional behaviour, range behaviour, checksums, pagination, encoding,
multipart behaviour, authentication —

```
Result_Stow(O) ≈ Result_S3(O)
```

Where feasible, differentially against real S3. The goal is not parity. The goal
is **no surprising divergence inside the advertised surface**.

A known live example of a divergence *outside* it: `ListObjectVersions` and
`GetBucketLocation` are absent from the 501 sub-resource list in
`internal/s3api/dispatch.go` and fall through to `400 InvalidRequest` where S3
returns `NotImplemented`. Any SDK feature-gate keying on the error code takes
the wrong branch. This is W10.

---

## 9. Cross-runtime conformance

The same conceptual operations should behave consistently across the Go runtime,
the S3 server, TypeScript, Python, WASM, and the browser profile. Where
interfaces differ *intentionally*, the difference is documented.

**One confirmed divergence, flagged in the original draft and verified here.**
The two clients obtain a bucket differently and neither difference is
documented as deliberate:

> **Correction.** The first draft of this section claimed that
> `packages/stow-s3-py/src/stow_s3/__init__.py:13` contained "an unconditional
> `s3.create_bucket(Bucket=bucket)`". It does not. That line is inside the
> module **docstring**, as a usage example. The Python client contains no S3
> call at all. The draft arrived at it by grepping for `create_bucket` and
> reporting the hit without checking whether it was code — the same
> read-instead-of-measure failure this document exists to prevent, committed by
> the document itself.

The real divergence runs the other way, and it is worse:

- **Python never creates a bucket.** `session.py:132` is explicit: "Creating the
  bucket is left to the caller, so this is a name and nothing more."
- **TypeScript creates it twice** — once server-side (`start.ts:316`, via
  `buckets: [bucket]` at `session.ts:125`) and again client-side with a
  `CreateBucketCommand` (`session.ts:137`), which is not idempotent against real
  S3 outside `us-east-1`.
- **The server was bent to accommodate the duplicate.**
  `internal/s3api/handlers_bucket.go:36` tolerates a repeat:
  `if err != nil && err != storage.ErrBucketExists`. Note that is `==`, not
  `errors.Is`, so a *wrapped* `ErrBucketExists` would escape as `InternalError`.

So: one client creates nothing, the other creates twice, and a protocol-layer
exception exists solely so the second create is not an error. "What happens when
the bucket already exists" is not merely undefined in both clients — in one it is
a special case in the server.

**A second, larger divergence: the two clients do not use the same store.**

| Client | Backend for an ephemeral session |
|---|---|
| TypeScript `session.ts:117` | `backend: "memory"` |
| Python `session.py:253` | `--backend filesystem` |

Two implementations of one session contract, selecting different stores. Combined
with [section 21.3](#213-coupling-audit-what-phase-2-found)'s findings that
memory and filesystem skip checksum verification while workspace performs it, and
that they differ in `ChecksumAlgorithm` casing and in how many times a body is
copied — a cross-runtime conformance test on "the same session" would be testing
two different systems. **§9's goal is currently unreachable**, and this is why.


---

## 10. Architectural invariants

These are the target. Four of them are **not currently true**, and
[section 21.3](#213-coupling-audit-what-phase-2-found) says which and why. An
invariant list that silently claims a violated property is worse than no list,
because it is checked by reading rather than by running.

1. **One logical object model.** Every backend implements the same logical
   object model. *Currently false: only `workspace` verifies checksums; the
   backends also differ in `ChecksumAlgorithm` casing, body-copy count, and
   derived `ContentType`.*
2. **Interfaces do not own storage.** S3, native, and filesystem interfaces
   translate into operations on the same namespace. *Currently inverted:
   `internal/s3api` imports `internal/runthrough` and finds policy by asserting
   on the store, so the interface layer owns policy instead.*
3. **Stores do not own protocols.** A store does not know which interface an
   operation arrived on. *True of imports and control flow; false of behaviour —
   S3's ETag algorithm, base64 checksums, `If-Match` grammar, reserved bucket
   prefixes, and the 1024/1000/10000 limits all live in `internal/storage`. See
   §3.2.*
4. **Policies compose.** Resource, lifetime, access, and upstream policies are
   independently configurable wherever technically meaningful. *Currently
   violated by the run-through cache being inside the store, which makes quota
   accounting include upstream bytes.*
5. **Dangerous authority is explicit.** Local→external mutation requires
   affirmative authority, separately from the policy that permits propagation.
   *Decided in [ADR 0005](../adr/0005-live-write-requires-explicit-consent.md),
   and holding.*
6. **Capabilities are discoverable.** Consumers inspect rather than assume.
   *Violated in the reporting path: readiness hardcodes `Multipart: true` and
   ignores `runtime.Capabilities`; the browser client overwrites capabilities
   with literals.*
7. **Profiles are configuration.** Profiles must not become divergent
   implementations. *True for the CLI. False for `pkg/stow` (two constructors) and
   for the browser path (a second persistence implementation). With the
   safety-conditional exception in §4.*
8. **One source of truth.** No synchronized duplicate representations of one
   namespace. *Holding.*
9. **Failure is bounded.** A failed environment leaks no processes, temporary
   directories, credentials, multipart state, claims, locks, or upstream
   mutations beyond documented semantics. *Holding, as of this change — the
   Python client leaked a caller-supplied directory and did not.*
10. **Existing clients remain ordinary clients.** Software should not need to
    know it is talking to Stow. This is the invariant that makes §1's closing
    claim — *the application does not need to know what Stow is, it simply
    receives storage* — an architectural commitment rather than a slogan.

---

## 11. What we should not build

This architecture does not imply Stow becomes a production distributed object
store, a complete AWS emulator, an S3 clone, a cloud control plane, a
multi-tenant public storage service, a distributed database, or a proprietary
storage protocol.

The primitive should be broad. The operational ambition should stay
disciplined. Stow's strength is making storage environments cheap to instantiate
and easy to control — not being a datastore.

---

## 12. API direction

Illustrative, not mandated.

```go
env, err := stow.Open(stow.Config{
    Store:    stow.Memory(...),
    Lifetime: stow.ProcessLifetime(...),
    Limits:   stow.Limits{MaxBytes: 64 << 20, MaxObjects: 1000},
    Access:   stow.LocalCapability(),
    Upstream: stow.NoUpstream(),
})

s3 := stows3.Serve(env, ...)
```

A filesystem-backed environment changes one line. A read-through environment
changes one line. The objective is that the internal architecture supports that
decomposition — not that this exact spelling ships.

**And it is not true of `pkg/stow` today, which the first draft missed.**
`stow.Options` (`pkg/stow/types.go:20`) has three fields — `Backend`, `MaxBytes`,
`MaxObjects` — and **no `Store` field**, so the `Store: stow.Memory(...)` shape
above does not exist. The reason is one layer down: `internal/runtime/types.go:72`
refuses any non-memory backend when no store is bound, so `pkg/stow` has to
expose a *second* constructor, `stow.OpenWorkspace`. `pkg/stow/doc.go:3` states
the consequence plainly — "Open exposes the memory-only embedded profile."

That is the "a constructor argument deciding an object-model capability" shape,
and it is the single change that would most directly deliver this section's
promise. `Options` gaining a `Store` field is a small diff with a large payoff:
it makes the embedded path composable rather than forked, and it is a precondition
for §4's profiles existing on the embedded side at all.


**Public API direction is the opposite constraint.** Do not expose the
complexity. The common path stays this small:

```ts
await withStow(async ({ s3, bucket }) => {
    // I have storage.
});
```

Progressive disclosure: `withStow()` → `withStow({ maxBytes })` →
`openStow({ backend, lifetime, upstream })` → primitive/native APIs.
Architectural generality must **reduce internal special cases without increasing
basic user complexity.** A refactor that achieves the first by spending the
second has failed.

---

## 13. Testing strategy

The test architecture should mirror the primitive.

**Store conformance.** Every store runs the same suite.

**What exists, and it is better than the first draft of this document claimed.**
`internal/storage/backend_contract_test.go` is a real package-local contract
suite: a `storeFactory` table over **memory, filesystem, and workspace**, with
nine behaviours each — batch delete, missing-bucket error agreement, metadata
aliasing, immutable version for equal content, multipart lookup, bucket deletion
with an active upload, duplicate and unsorted completion parts, and
`ListParts` pagination markers. `conformance/` adds a shared corpus run across
backends *and* runtimes, wired into `make test-conformance` and CI.

The first draft said `internal/storage` had no contract test and proposed
creating one as the P0. Both halves of that were wrong, and the correction
matters more than the original: the suite exists, so the real question is where
it has holes.

**The hole is checksums, and it is a shipping divergence.**
`grep -c Checksum internal/storage/backend_contract_test.go` returns **0**. And
per [section 21.3](#213-coupling-audit-what-phase-2-found), only the
workspace store verifies a caller-supplied checksum — memory and filesystem
store the algorithm and value verbatim and never check them. So a corrupt body
is accepted by two of three stores and rejected by the third, and the contract
suite cannot see it. Adding a checksum case to the existing suite is a few lines
and goes **red immediately**.

**The second hole is subtler.** `TestStoreListPartsPaginationMarkers` calls
`ListPartsPage` — but `ListPartsPage` is **not in `storage.Store`**. The test
therefore passes on the three concrete types while asserting a property the
*interface* does not have, which is why `internal/runtime/multipart.go:104` has
to find it by type assertion and run-through mode silently does not have it. A
contract suite that reaches past the interface it is a contract for is testing
the implementations, not the contract.

**Interface conformance.** S3 behaviour gets its own protocol suite, separate
from store conformance — a store can be correct and its S3 rendering wrong.

**Policy conformance.** Lifetime, limits, and upstream policies get reusable
behavioural tests. This is where invariant 9 gets enforced.

**Composition tests.** Test the combinations, not just the components:
store×interface, store×lifetime, store×quotas, store×upstream policy,
interface×authentication, lifetime×failure mode. Section 21.2 is a worked
example of what skipping this costs, and the two holes above are both
composition failures wearing a store costume.

---

## 14. Property testing

Property-based tests for the object model, runnable against every conforming
backend.

| Property | Statement |
|---|---|
| Put/Get | after `Put(B,K,X)`, absent intervening mutation, `Get(B,K) = X` |
| Delete | after `Delete(B,K)`, `Head(B,K) = NotFound` |
| Quota | for quota `Q`, `usage(S) ≤ Q` after every successful operation |
| Isolation | for independent namespaces A and B, `Mutation(A) ⇏ StateChange(B)` unless a relationship is configured |

The isolation property is the one that would have caught the run-through
defect, and the quota property is the one that would have caught §2's unraisable
request cap being advertised as raisable.

---

## 15. Failure injection

Stow needs aggressive failure testing, because lifecycle and upstream
coordination are its distinguishing features. Inject failure during startup,
readiness negotiation, `PutObject`, multipart upload, filesystem rename,
persistence, upstream request, outbox claim, outbox completion, shutdown,
cleanup, and parent death — then assert the environment returns to a *documented*
state.

The three the current tree is weakest on: filesystem rename (a workspace write
is a rename away from durable), outbox claim/completion (upstream mutation
exactly-once), and parent death (the §3.4 guarantee that is silently absent on
Windows today).

---

## 16. Observability

Observability should describe the primitive, not expose implementation
accidents. Useful signals: environment lifetime, object count, bytes used, quota
utilization, operation count and latency, cache hits/misses, upstream
operations, outbox depth, failed propagation, cleanup duration.

Diagnostics should answer: *what environment exists, what can it do, and what
state is it in?* `cmd/stow-s3/doctor.go` is the existing entry point.

---

## 17. Performance objectives

Stow should not be optimized primarily for bulk-storage throughput. For the
primary primitive, prioritize startup latency, memory per environment, cleanup
latency, concurrent environment count, first-operation latency, and predictable
resource usage.

Two first-class benchmarks:

```
M(n)     = memory required for n isolated environments
T_start  = time to first usable capability
```

And the question that actually matters:

> How many independent Stow capabilities can an ordinary developer machine or a
> CI worker safely issue at once?

A GitHub-hosted runner is the honest denominator. It is the environment Stow is
most often asked to work in, and nobody currently knows the answer.

---

## 18. Security model

Stow should be documented in capability-oriented language. An environment grants
some combination of read, write, delete, enumerate, administer, and
propagate-upstream. The architecture should make these independently
constrainable.

Three non-assumptions, all carried forward from current design:

- Holding S3 credentials does not imply administrative authority.
- Being able to reach an upstream does not imply being allowed to mutate it.
- Localhost does not imply trust.

---

## 19. Migration plan

Evolutionary. **Do not rewrite Stow from scratch.** Each phase below names the
existing work it maps onto, because most of it is already partly done.

| Phase | Deliverable | Maps to |
|---|---|---|
| 1 | This document | — |
| 2 | Coupling audit: where S3 leaks into stores, where backend selection changes unrelated behavior, where upstream logic leaks into storage, where lifecycle lives in clients, where modes fork behavior | §20 |
| 3 | Store conformance becomes the enforceable definition of a store | §13 |
| 4 | S3 semantics clearly above the primitive | §8 |
| 5 | Lifetime, limits, access, upstream extracted as explicit concepts | §3.4–§3.7 |
| 6 | Modes become profiles internally; user-facing modes stay for compatibility | §4 |
| 7 | Readiness expanded into the versioned environment descriptor | §3.8 |
| 8 | Cross-runtime fixtures: Go, HTTP/S3, TypeScript, Python, WASM | §9 |
| 9 | Differential S3 harness against an authoritative implementation | §8 |
| 10 | Delete obsolete special-case code — **this is where the maintenance dividend starts** | — |

---

## 20. Priorities, reconciled with the delivery plan

The original draft of this document carried its own priority list. It was
written without reference to `docs/agent-dx-plan.md` §0.11, and the two
disagreed: this document proposed P0 architecture work while §0.11 named W5 (the
S3 facade) as the most valuable buildable work and W14 (the accounts) as
gating everything.

**§0.11 wins, and this document is subordinate to it.** W14 is a distribution
problem with the longest lead time in the plan and no code change shortens it;
the wedge Stow is competing on is distribution, and a distribution claim that
cannot be `pip install`ed is not one. This document does not reorder that.

The architecture items map onto §0.11's W-items rather than competing with them:

| This document | Existing item | Relationship |
|---|---|---|
| §3.5 resource policy | **W6** | quotas a host can actually set, including the unraisable request cap — §2 gap 1 |
| §3.4 lifetime | **W12** | the Windows parent-death watch — §2 gap 2 |
| §3.7 upstream policy | **W9** | blocked on fixing run-through, which §21.2 measures |
| §8 S3 surface | **W10** | the two sub-resources returning the wrong error code |
| §1, §3, §4, §12 | **W5** | the S3 facade is the primitive made usable from generated code |
| §13 store conformance | — | new; the cheapest real win in this document |

**The one genuinely new P0, and the first draft got it wrong.** It proposed making
store conformance enforceable, on the stated premise that `internal/storage` had
no contract test. It does — `backend_contract_test.go`, nine behaviours across
all three stores. The real gap is narrower and sharper:

> **Add a checksum case to the existing store contract suite.** It goes red
> immediately, and it exposes that memory and filesystem accept corrupt bodies
> while workspace rejects them.

That is a few lines of test against a real integrity divergence that is shipping
now, and it is the cheapest honest win in this document. The broader "make
conformance the definition of a store" work is still worth doing — it just is
not a blank page, and describing it as one would have sent someone to rebuild
something that exists.

**A second new item, from the audit:** the two clients select different backends
for the same session contract (§9), and the Python client deleted a
caller-supplied directory on close. The second is fixed; the first is not, and it
is what currently makes §9 unreachable.


---

## 21. Open defects this document inherits

### 21.1 Already-known, tracked elsewhere

The §0.6 list of eleven inherited defects in `docs/agent-dx-plan.md` stands. So
does the `MaxRequestBytes` gap (W6), the Windows parent watch (W12), and the two
S3 sub-resources (W10).

### 21.2 Run-through: measured, and worse than "untested"

The original draft asserted read-through as an existing capability. It was
measured on 2026-09-26 and it is not one.

```go
local, cache, up := memory(), memory(), upstreamWith("bucket", "k", "upstream-bytes")
a := NewWithOutbox(Config{Policy: PolicyReadThroughCache}, local, cache, up, nil)
a.GetObject(ctx, "bucket", "k")
// err = "bucket not found", upstream get calls = 0
```

Both stores report `ErrBucketNotFound` — never `ErrObjectNotFound` — for a
bucket that does not exist (`internal/storage/fs/fs.go`, `internal/storage/memory.go`).
`Adapter.resolveObject` (`internal/runthrough/adapter.go:305`) treats that as a
final answer rather than a cache miss, so the upstream is only ever consulted
when the key is *already present* locally. Writes fail earlier still: the
quota pre-check calls `HeadObject` first, so a write 404s before the outbox is
reached.

This survived 75 passing tests at 66.3% statement coverage for two structural
reasons, both worth carrying as rules:

1. **The fixture made the failing branch unreachable.** All 26 `NewWithOutbox`
   call sites in `internal/runthrough` pass **the same store as both `local` and
   `cache`**. Production passes two distinct stores — `cmd/stow-s3/store.go:37`
   opens a second store at `cfg.CacheDir`. With `cache == local`, a cache lookup
   can never fail independently of the local one, so the branch that consults
   the upstream is never taken. The only tests that pass distinct stores use
   `NewWithCache` (5 sites, in `cache_policy_test.go` and `cache_list_test.go`),
   and every one of them pre-creates the bucket in *both* stores:

   ```go
   _ = local.CreateBucket(ctx, "bucket")
   _ = cache.CreateBucket(ctx, "bucket")
   ```

   which makes the cache agree with local about which buckets exist — the same
   crutch, one level down. **When a constructor takes two dependencies of the
   same interface, compare what the tests pass against what production passes.**
   If they differ, an entire configuration is untested.
2. **Green is not evidence that the test exists.** A conforming test would have
   made this red. It did not, and the gap between "75 tests pass at 66%" and
   "read-through works" is the entire argument for [section 13](#13-testing-strategy)'s
   composition tests.

The fix pattern is a harness whose *default* is the broken case — one that
creates no buckets — so a test must opt in to seeding rather than being asked to
remember not to.

A workspace with a read-through upstream is **W9**, and until this is fixed W9 is
blocked rather than merely unscheduled.

### 21.3 Coupling audit: what Phase 2 found

Phase 2 of the migration plan in §19 was performed on 2026-09-26 rather than
scheduled, and it changed this document twice: it corrected §3.2's "it is a naming
problem", and it corrected §20's claim that no store contract suite existed.

**What is genuinely clean, and should be stated as plainly as the findings:**

- `internal/storage` imports nothing from the project. `internal/runthrough`
  imports only `internal/storage`. No store file contains an HTTP status code, an
  XML type, an AWS SDK type, SigV4, or `smithy`. `mapStorageError`
  (`s3api/errors.go:106`) is a clean one-way translation table. **The dependency
  direction is correct.**
- The readiness protocol is **exemplary** and the doc undersold it. Three
  implementations — `internal/ready/ready.go`, `packages/stow-s3/src/ready.ts`,
  `packages/stow-s3-py/src/stow_s3/ready.py` — agree **field for field** on all
  fourteen fields, all implement `ProtocolVersion = 1`, all reject unknown
  versions, missing fields, and non-finite numbers, and both clients correctly
  read `0` as "no limit reported" rather than "unlimited". Duplicated, and in
  agreement. This is the model for §3.8.
- CLI mode selection is configuration, not a fork, and the one
  backend-conditional inside it (`main.go:171`) is a justified safety invariant.
- The upstream/cache code is a **decorator above** the store, not inside it. No
  file in `internal/storage/**` mentions upstream, cache, TTL, eviction, outbox,
  or policy.

**The findings, ranked by consequence.** Each was verified against the tree
rather than inferred:

1. **The resource policy is structurally blind to the upstream relationship.**
   Because the cache lives *inside* the store, `runtime.Instance.usage` is
   bootstrapped from `|local ∪ cache ∪ upstream|` (`runtime/state.go:98` walks
   `ListObjectsV2`, and in production that store *is* the run-through adapter),
   and every later `objectSize()` routes through `HeadObject` → `resolveObject` →
   possibly the cache. So `state.go:102` can return `ErrQuotaExceeded` **at
   startup** because of upstream state. A relationship is being charged as
   resource usage. This is the concrete, shipping cost of the `Store +
   UpstreamPolicy` shape §3.7 says was not chosen.
2. **Only the workspace store verifies checksums.** `workspace/objects.go:54`
   calls `verifyChecksum`; `memory.go` and `fs/fs.go` contain **zero**
   occurrences and store the algorithm and value verbatim. A corrupt body is
   accepted by two of three stores. This falsifies invariant 1 and is invisible to
   the contract suite, which has no checksum case.
3. **The interface layer owns policy.** `internal/s3api/admin.go:12` imports
   `internal/runthrough` and reaches the outbox through **16** structural type
   assertions on `s.store.(…)`. The layering violation runs **upward**, which is
   the direction §3.3 does not look for. `s3api.Config` even carries `Mode`,
   `CachePolicy`, `WritePolicy`, and `UpstreamHost` purely to echo them into
   `/_stow/status`.
4. **Multipart availability is a constructor flag, and readiness lies about
   it.** `runtime/adapter.go:46` passes `multipartEnabled = true`
   unconditionally while `runtime/instance.go:41` passes `false` — even though
   `MemoryStore` fully implements multipart. Meanwhile `main.go:70` hardcodes
   `Multipart: true` in the readiness message and never consults
   `runtime.Capabilities`, which already computes both. Invariant 6 implemented
   by assuming.
5. **The two clients pick different backends** for the same session contract
   (§9), and the Python client's `close()` deleted a caller-supplied directory.
   **Fixed** in this change, with a test that goes red on the old code.
6. **`ListPartsPage` is outside `storage.Store`** (§3.2), so run-through mode
   silently takes the unbounded-memory path while the contract suite asserts the
   capability exists.
7. **Default quota depends on which constructor the embedder chose.**
   `--max-bytes 0` means *unlimited* on the server (`runtime_store.go:22`) and
   *64 MiB* in `pkg/stow.Open` (`runtime/types.go:22`). Same class as §2's
   `MaxRequestBytes` gap and belongs beside it.
8. **Workspace and browser are profile forks.** `OpenWorkspace`
   (`pkg/stow/workspace.go:78`) is a bespoke constructor; the browser path
   (`packages/stow-s3/src/persistent-embedded.ts:38`) is a complete second
   persistence implementation in TypeScript, with its own state machine, layered
   on the WASM runtime — and it **overwrites the runtime's reported capabilities**
   with literals, including `multipart: false`. §4 and §7 do not currently hold
   for either.

**What this does to the invariants.** Invariant 1 is false (finding 2).
Invariant 2 is half-wrong: interfaces do not own storage, but they now own
*policy* (finding 3). Invariant 3 holds for imports and is violated for behaviour
(§3.2). Invariant 7 does not hold for workspace or browser (finding 8). That is
four of ten, and the point of recording it is that the remaining six are the ones
worth defending.


---

## 22. Success criteria

We have succeeded when a new use case is usually a *composition* rather than an
implementation.

A 32 MiB temporary filesystem-backed store, visible through S3, that reads misses
from R2 and can never modify R2:

```
Store      = Filesystem(temp)
Interface  = S3
Lifetime   = Scope
Quota      = 32 MiB
Upstream   = ReadThrough(R2)
LiveWrites = false
```

No new kind of Stow.

An agent gets a 100 MiB workspace reachable through both shell tools and S3, for
the duration of one task:

```
Store      = WorkspaceFilesystem
Interfaces = Filesystem + S3
Lifetime   = Task
Quota      = 100 MiB
Upstream   = None
```

Also no new kind of Stow.

**If every new scenario requires a new mode, the architecture has failed. If most
can be expressed as compositions, it is working.**

---

## 23. Engineering decision rule

For any proposed feature, in order:

1. **A new primitive dimension?** Then define its contract.
2. **A new implementation of an existing dimension?** Then make it satisfy the
   existing conformance suite.
3. **A useful composition?** Then implement it as a profile or configuration.
4. **Does it need a special-case Stow mode?** Stop and find out why. If the
   conditional guards a safety invariant (§4), keep it and document it as one.
5. **Does it grant new authority?** Make that authority explicit.
6. **Can the environment advertise the result?** If not, extend §3.8.
7. **Does it preserve one source of truth?** If not, reconsider.

This is the default review framework for Stow work. It is deliberately the same
shape as the invariants in §10, so a feature that passes the questions satisfies
the invariants by construction.

---

## 24. The destination

```
                    STOW ENVIRONMENT
                           │
          ┌────────────────┼────────────────┐
          │                │                │
       Lifetime          Policy         Interfaces
          │                │                │
          │        ┌───────┼───────┐    ┌───┼────┐
          │      Quota   Access Upstream  S3  FS Native
          │                │                │
          └────────────────┼────────────────┘
                           │
                       Namespace
                           │
                          Store
                           │
                ┌──────────┼──────────┐
              Memory   Filesystem   Workspace
```

Everything above is composable. Everything below is conformant.

S3 remains the interoperability superpower that lets ordinary software consume
the capability without knowing Stow exists. And the simplest experience stays
simple:

```ts
await withStow(async ({ s3, bucket }) => {
    // I have storage.
});
```
