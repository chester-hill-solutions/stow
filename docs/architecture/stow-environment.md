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
marked with evidence, and section 21.2 records what to do about it.

Everything else in the list held up. Section 2 is a table with a verdict and a
reference for each row, so a future reader can re-run the check rather than
trust the table.

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
| Browser application | WASM | native | explicit | host-set | none |
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

**The mechanism is clean.** No store implementation knows about HTTP, XML, S3
status codes, or SigV4. The storage error vocabulary (`internal/storage/errors.go`)
is object-model vocabulary — `ErrBucketNotFound`, `ErrPreconditionFailed`,
`ErrChecksumMismatch`. Translation to S3 error codes happens above. This is the
separation the store contract is for, and it largely holds.

**The vocabulary is S3-shaped, and that is a real coupling.** The interface
declares `ListObjectsV2` — the S3 API name, not a generic one — plus
`CopyObject`, `DeleteObjects`, and **eight** methods dedicated to multipart
upload. Multipart is arguably a legitimate object-model concept (streaming an
object larger than memory) that S3 also happens to expose. `ListObjectsV2` is
not: it names an S3 API generation inside the storage contract. The original
draft of this document proposed a `Reset()` method; no such method exists, and
the draft's interface omitted all eight multipart methods, which is how a
document ends up describing a system that was never built.

The distinction worth preserving: **stores must not know what protocol a request
arrived on.** They currently do not. The S3-shaped *naming* is a smaller
problem than S3-shaped *behaviour*, and it is a naming problem.

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

- `packages/stow-s3-py/src/stow_s3/__init__.py:13` — an unconditional
  `s3.create_bucket(Bucket=bucket)`.
- `packages/stow-s3/src/session.ts:137` — `CreateBucketCommand`, which against
  real S3 is *not* idempotent outside `us-east-1`.

Language ergonomics may differ. Primitive semantics should not. Specifically:
what happens when the bucket already exists should be the same answer in both
clients, and today it is not defined in either.

---

## 10. Architectural invariants

1. **One logical object model.** Every backend implements the same logical
   object model.
2. **Interfaces do not own storage.** S3, native, and filesystem interfaces
   translate into operations on the same namespace.
3. **Stores do not own protocols.** A store does not know which interface an
   operation arrived on. *Currently holds in mechanism; partly violated in
   vocabulary — see §3.2.*
4. **Policies compose.** Resource, lifetime, access, and upstream policies are
   independently configurable wherever technically meaningful.
5. **Dangerous authority is explicit.** Local→external mutation requires
   affirmative authority, separately from the policy that permits propagation.
   *Decided in [ADR 0005](../adr/0005-live-write-requires-explicit-consent.md).*
6. **Capabilities are discoverable.** Consumers inspect rather than assume.
7. **Profiles are configuration.** Profiles must not become divergent
   implementations. *With the safety-conditional exception in §4.*
8. **One source of truth.** No synchronized duplicate representations of one
   namespace.
9. **Failure is bounded.** A failed environment leaks no processes, temporary
   directories, credentials, multipart state, claims, locks, or upstream
   mutations beyond documented semantics.
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

**Store conformance.** Every store runs the same suite: memory, filesystem,
WASM, future backends.

**What already exists:** `conformance/` — a shared corpus run across backends and
runtimes, wired into `make test-conformance` and into CI.

**What is missing, and it is the P0 in §20:** the conformance corpus is a
separate package. `internal/storage` has no package-local contract test. A new
store can satisfy the `Store` interface, compile, wire up, and never be run
against the corpus — because nothing forces a store to *be* a store
conformance-wise. Making the corpus the enforceable definition of a store means
`go test ./internal/storage/...` fails for a store that does not pass it.

**Interface conformance.** S3 behaviour gets its own protocol suite, separate
from store conformance — a store can be correct and its S3 rendering wrong.

**Policy conformance.** Lifetime, limits, and upstream policies get reusable
behavioural tests. This is where invariant 9 gets enforced.

**Composition tests.** Test the combinations, not just the components:
store×interface, store×lifetime, store×quotas, store×upstream policy,
interface×authentication, lifetime×failure mode. Section 21.2 is a worked
example of what skipping this costs.

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

**The one genuinely new P0, and it is small:** make store conformance
enforceable (§13). It is a few hours' work, it makes §3.2's claims about stores
verifiable rather than asserted, and it is the thing that stops the next store
from being written against the interface instead of against the model.

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
              Memory   Filesystem   WASM/other
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
