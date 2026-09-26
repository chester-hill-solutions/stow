# Stow Environment — Implementation Plan

**Status:** proposed
**Date:** 2026-09-26
**Relationship:** execution plan for
[`docs/architecture/stow-environment.md`](stow-environment.md) (the primitive), and
a correction to the 15-phase sequence that document was drafted from. Where this
plan and that document disagree about *order*, this plan wins; where they
disagree about *shape*, the document wins.
**Also supersedes:** the implicit assumption that `docs/agent-dx-plan.md` §0.11 is
the only delivery order. It still is for product work. This plan is the
engineering order for the primitive, and §8 says how the two interleave.

This is a work plan, not an essay. Every item names a target, a verifiable
condition, and its blocking edges. Items marked **[audit first]** must not be
implemented until a Phase-2-style coupling audit has produced the map; the
roadmap that produced this document skipped that step, and skipping it is how
`internal/runthrough` became 3,646 lines with a dead read path.

---

## 0. How to read this

- **Milestones** M1–M4 match the four in the roadmap, with different contents.
- **Work items** are `M<n>.<n>`, independently grabbable, each declaring edges.
- **Grounding.** Every "target" below was read in the tree on 2026-09-26. Where a
  claim is inferred rather than measured it says so. §2 lists what is already
  built so nobody rebuilds it; §3 lists what is already broken so nobody
  assumes it works.
- **Effort** is in days and is a deliberately rough lower bound.

---

## 1. The corrected ordering

The roadmap's Phases 3–7 read as *security work that precedes the interesting
performance work*. They are not. They are the **precondition** for it.

You cannot run N environments in one process until Environment A provably cannot
reach Environment B (namespace and filesystem walls) and cannot exceed its own
grant (quotas that hold under concurrency). Logical isolation *is* the security
work. Phase 10 — "attack the biggest obstacle to the agentic thesis" — is not a
later optimization; it is gated by everything before it.

So the ordering is right, but the framing inverts its own priority. This plan
makes the dependency explicit so the security milestones and the performance
milestone stop competing for the same slot.

Three reorderings against the 15-phase list:

| Roadmap | Here | Why |
|---|---|---|
| Phase 1 writes 3 new documents | **No new documents.** Edits to `stow-environment.md` | §10, §18, §3.7 of that file already *are* the invariants, security model, and authority split. Three new files drift from the one we just spent two PRs correcting. |
| Phase 2 wraps Runtime in `Environment` | Split: delete-first, then wrap (§4) | `cmd/stow-s3/main.go:70` already computes capabilities a second time. Wrapping before deleting creates a *third* source of truth — invariant 7's exact failure, introduced by the refactor meant to establish it. |
| Phase 6 designs reservation semantics | **Verify what exists** (§4, M2.3) | `Committed + Reserved ≤ Limit` is already the implementation at `internal/runtime/state.go:19`. |

---

## 2. What is already built — do not rebuild

| Thing | Where | Note |
|---|---|---|
| Store contract suite | `internal/storage/backend_contract_test.go` | 9 behaviours × 3 stores. **No checksum case** — the hole is in the suite, not its absence. |
| Cross-runtime conformance corpus | `conformance/` | Runs memory/filesystem × memory/filesystem/runtime backends. Wired to `make test-conformance` and CI. |
| Quota reservation | `internal/runtime/state.go:18-19,52,82,156-161` | `usage.Bytes + reservedBytes - oldSize + newSize <= MaxBytes`, with `reservedTargets`, `reservedObjects`, a mutex. |
| Idempotent disposal | `internal/runtime/instance.go:470` | `closeOnce` + mutex. |
| Readiness descriptor | `internal/ready/ready.go` | Three implementations, **field-for-field identical**, all `ProtocolVersion = 1`, all strict. The model for the environment descriptor. |
| Path containment machinery | `internal/storage/workspace/layout.go` | Escape markers, reserved names, natural/escaped forms, union-of-host rules. Needs auditing, not building. |
| Race detector in CI | `.github/workflows/ci.yml:50` | `make test-race` already runs. |
| Identity substrate | `internal/storage/workspace/registry.go` | `Register`/`Lookup`/`Forget`/`All`. Promotion (M4.3) builds on this. |
| Ownership marker discipline | `internal/storage/fs/owner.go` + `scripts/check-version.mjs` | A store constant kept in sync with a client `rm -rf` by a CI ratchet. Correctly disclosed. |

## 3. What is already broken — do not assume it works

| Defect | Evidence | Consequence |
|---|---|---|
| **No authorization below the interfaces** | `grep -riE "authoriz" pkg/stow/*.go internal/runtime/*.go` → **no matches** (nor `permission`, `allowed(`, `acl`). Auth exists only in `internal/s3api/auth.go` (42 lines) and `server.go:331 authorizeAdmin`. | The embedded native path has **no access control at all**. SigV4 is an S3-interface mechanism. This is M1.3 and it is the most serious hole in the codebase. |
| Read-through never reads through | `internal/runthrough/adapter.go:305`; measured: upstream-only bucket → `bucket not found`, 0 upstream calls | 23% of production Go is non-functional. W9 blocked. |
| Quotas blind to upstream | `internal/runtime/state.go:98` walks the store, which in production *is* the run-through adapter | Startup can return `ErrQuotaExceeded` because of upstream state. |
| Only 1 of 3 stores verifies checksums | `workspace/objects.go:54` calls `verifyChecksum`; `memory.go` and `fs/fs.go` have **zero** occurrences | A corrupt body is accepted by two of three stores. **Shipping.** |
| Capability duplication | `cmd/stow-s3/main.go:70-77` recomputes `Persistent`/`Multipart`/`Upstream`; `runtime/instance.go:80` already computes them; `runtime.Capabilities` never consulted | Invariant 6 implemented by assuming. Also readiness hardcodes `Multipart: true` where `pkg/stow` passes `false`. |
| Two clients, two backends | `session.ts:117` `memory` vs `session.py:253` `filesystem` | §9 cross-runtime conformance is currently unreachable. |
| `ListPartsPage` outside the interface | Not in `storage.Store`; found by assertion at `runtime/multipart.go:104`; `runthrough.Adapter` lacks it | Run-through silently takes unbounded-memory pagination. Contract suite asserts a property the interface doesn't have. |
| S3 conditional writes | Open issue **#11**: `If-None-Match: *` returns 200, silently overwriting | Correctness bug in the advertised surface. |
| Interface layer owns policy | `internal/s3api/admin.go:12` imports `internal/runthrough`; 16 structural assertions on `s.store.(...)` | The layering violation runs **upward**. |

---

## 4. M1 — Environment Core

**Outcome:** Stow knows what it is allocating, and enforcement lives below the
interfaces. No meaningful API break.

Estimated 8–12 days. This is the milestone I would execute next, and the order
inside it is load-bearing.

### M1.1 — One source of truth for capabilities
**Why:** `cmd/stow-s3/main.go:70-77` recomputes what `internal/runtime/instance.go:80`
already computes, and hardcodes `Multipart: true` where `pkg/stow` passes `false`.
**Target:** delete the recomputation; read `runtime.Capabilities`.
**Done when:** no capability string comparison remains in `cmd/`; a test asserts
the readiness payload's `multipart` equals the runtime's, for both constructors.
**Blocks:** M1.4, M1.6. **Effort:** <1 day.
**Why first:** every other item adds a consumer of capabilities. Adding one while
two disagree is how you get three.

### M1.2 — `Store` field on `pkg/stow.Options`
**Why:** `Options` (`pkg/stow/types.go:20`) has no `Store` field and
`runtime/types.go:72` refuses any non-memory backend without a bound store, so a
second constructor (`OpenWorkspace`) exists. That is why "a filesystem-backed
environment changes one line" is false today.
**Target:** `pkg/stow/types.go`, `internal/runtime/types.go:68-75`.
**Done when:** `pkg/stow.Open` accepts a bound store for every backend the
runtime recognises; `OpenWorkspace` is expressible through `Options` (keep it as
a thin wrapper — the API break is the point of "no meaningful break": it must not
be one).
**Blocks:** M1.4, M3.1. **Effort:** 2–3 days.

### M1.3 — Authority below the interfaces ⚠️ highest severity
**Why:** the embedded path has no authorization. SigV4 protects the S3 surface
only, so a native caller is unconstrained. Today that is "by design" because
`pkg/stow` is same-process; it stops being by design the moment M3 puts N
environments in one process, and M3 is the whole thesis.
**Target:** new `internal/authority` package. An `Operation` enum
(`object.read/write/delete/list`, `environment.reset/destroy`,
`upstream.read/write`, `environment.promote`), an `Authority` value, and a single
`Allows(op)` predicate. `internal/runtime.Instance` gains an authority field and
checks it in its mutating methods. `internal/s3api` becomes a *translator*:
SigV4 authenticates, then maps the authenticated principal to an `Authority`.
**Done when:**
- `grep -riE "authoriz" internal/runtime/*.go` returns matches
- an operation refused for a given `Authority` is refused identically through S3, native, and WASM
- `DevBypass` (`internal/s3api/auth.go:14`) cannot widen authority
- a test asserts the native path refuses what the S3 path refuses
**Blocks:** M1.4, M2.1, M2.4, M3.1, M4.1. **Effort:** 3–4 days.
**Note:** the invariant is `Allowed(op) = Environment.Authority.Allows(op)`.
One predicate, called below the interfaces, never in an adapter.

### M1.4 — `Environment` struct, delegating
**Why:** the roadmap's Phase 2, done after the deletions it depends on.
**Target:** `Environment{ Runtime, Namespace, Authority, Limits, Lifetime,
Upstream, Capabilities }`, delegating to existing code. No behaviour change.
**Done when:** `withStow()` behaves identically; the S3, workspace, native, and
WASM paths all construct an `Environment`; **no** capability or limit is computed
in two places (M1.1 is the deletion that makes this true).
**Blocks:** M2.2, M3.2. **Effort:** 2–3 days.

### M1.5 — Identity, credential, authority, upstream-credential
**Why:** the roadmap's Phase 4, generalized. Four things currently conflated as
"access". Must not imply one another: knowing an environment ID ≠ access; access
≠ administer; local write ≠ upstream write.
**Target:** distinct types, not four fields on one struct. Environment ID from the
registry. S3 credential authenticates a caller. `Authority` (M1.3) describes
permitted effects. Upstream credential authenticates Stow to external storage.
**Done when:** no code path grants admin because a caller presented a valid S3
credential; the live-write opt-in (`STOW_ALLOW_LIVE_WRITES`,
`cmd/stow-s3/main.go:171-179`) is re-expressed as `Authority` rather than a
separate flag.
**Blocks:** M2.4, M4.1. **Effort:** 2–3 days.

### M1.6 — Profiles as compositions
**Why:** the roadmap's Phase 8. Modes are a two-value enum
(`internal/runthrough/config.go:15`).
**Target:** `withStow()`, workspace, and read-through expressed as
`Environment` configurations. `cmd/stow-s3/store.go:24-26`'s mode branch becomes
configuration selection.
**Done when:** adding a profile requires no `if profile == X` outside profile
definition. **`cmd/stow-s3/main.go:171` is explicitly exempt** — it guards a
safety invariant (upstream propagation needs a durable local copy) and is
documented as such in `stow-environment.md` §4. Do not flatten it.
**Blocks:** M3.3. **Effort:** 2 days.

### M1.7 — Fix the two cheap correctness bugs
**Why:** both are shipping, both are small, and both are in M1's blast radius.
- **Checksum contract case.** Add to `backend_contract_test.go`. Confirm **red**
  on current code. Then make `memory.go` and `fs/fs.go` verify like `workspace`
  does. *This is the single cheapest integrity fix available.*
- **Issue #11.** `If-None-Match: *` must return 412, not 200.
**Done when:** contract suite covers checksums; #11 closed with a test that was
red first.
**Effort:** <1 day each.

---

## 5. M2 — Sandbox Core

**Outcome:** Stow can defensibly call an Environment a storage sandbox. This is
the gate for using that word in public.

**M2.1, M2.2, M2.5 are [audit first].** Do not write a plan for filesystem
hardening, lifetime enforcement, or revocation from a list of attack categories.
Audit the existing machinery, then plan. `layout.go` already has escape markers,
reserved names, and union-of-host rules; the question is what it misses, and that
is not answerable by reading a threat list.

### M2.0 — Sandbox conformance suite **[audit first]**
Restructure `conformance/` into `store/ s3/ environment/ sandbox/`. The sandbox
suite assumes a hostile workload. Categories: namespace escape, filesystem
escape, quota escape, credential escalation, admin escalation, upstream
escalation, lifetime escape, cross-environment interference, malformed requests,
concurrency attacks, crash/recovery, cleanup leakage.
**Done when:** the suite exists and runs under `-race`. **There is currently no
fuzzing anywhere in the repository** (`grep -rn "func Fuzz"` → no matches) and
`-race` is already in CI, so fuzz targets are additive and cheap to wire.
**Blocks:** M2.6. **Effort:** 2–3 days for the skeleton, then per-category.

### M2.1 — Namespace wall **[audit first]**
Environment A cannot discover, list, read, write, delete, or infer B.
**Likely mostly intact** — each session gets its own process and data directory
today, so the risk is filesystem traversal, not logical cross-talk. Audit before
building; the deliverable may be "one traversal bug" rather than a subsystem.
**Done when:** explicit A-attacks-B tests exist and pass.

### M2.2 — Filesystem wall **[audit first]**
Centralize path resolution. Assert `resolved(key) ∈ root(environment)`.
Adversarial: `..`, absolute paths, encoded traversal, symlinks, hard links,
Unicode normalization, Windows separators, UNC paths, case folding, rename races,
TOCTOU, long keys, concurrent mutation. **Fuzz this** — it is the one component
where fuzzing clearly beats enumeration.
**Note:** the union-of-all-hosts' rules decision (`stow-environment.md` §3.1) means
Windows rules are testable on Linux. Keep that.

### M2.3 — Make limits adversarially correct (verify, don't design)
**The invariant is already implemented** at `internal/runtime/state.go:19`. This
item is verification and gap-closing:
- 100 concurrent PUTs; multipart races; overwrite races; DELETE/PUT races;
  aborted uploads; disk errors; process death mid-write; retries
- **`[audit first]`** whether a crash between reserve and commit leaks a
  reservation. `state.go:52 reserveTarget` / `:31` release and the
  `resetStore` path at `instance.go:464-466` suggest a recovery story exists —
  confirm it, don't assume it.
- **Also in scope:** the two §3.5 gaps. `MaxRequestBytes` is a hardcoded 8 MiB
  with no flag (`internal/s3api/errors.go:63`); and default quota differs by
  constructor — unlimited on the server (`runtime_store.go:22`) vs 64 MiB in
  `pkg/stow.Open` (`runtime/types.go:22`).
**Done when:** a hostile concurrent suite passes under `-race`; no path exists
where usage exceeds the grant.

### M2.4 — Upstream wall
Require `PolicyAllows(op) ∧ AuthorityAllows(op)` before touching upstream.
Configuration alone must never manufacture authority.
**Done when:** no code path reaches an upstream without both; the M1.5 rewrite
of `STOW_ALLOW_LIVE_WRITES` is the template.

### M2.5 — Lifetime wall **[audit first]**
After expiry, revocation, or destruction: `Allowed(op) = false` for all
subsequent operations. Audit what ADR 0009 and the TTL collector already
guarantee before building.
**Open question this plan cannot answer:** the parent-death guarantee is true on
Linux and macOS and **absent on Windows** (W12). Whether a lifetime guarantee
that silently does not apply on one platform is acceptable is a product decision,
not an engineering one.

### M2.6 — Public "sandbox" claim
Only after M2.0 is green. The word is gated on the suite, not on intent.

---

## 6. M3 — Agentic Runtime

**Outcome:** `stow()` costs what `mkdtemp()` costs.

Gated on M1.3 (authority) and M2.1/M2.3 (confinement and quotas). This is the
milestone the thesis lives on, and it cannot start before them.

### M3.0 — Establish the cost baseline **[do first, before optimizing anything]**
Measure today: process spawn (`packages/stow-s3/src/start.ts:288`), readiness
handshake, `T_create`, `T_destroy`, `M_env`, and 100/1,000/10,000 environment
density. **There is no environment-density benchmark in the repo today** —
`docs/benchmarks/` holds throughput work, not allocation cost. Without a baseline,
"cheaper" is unfalsifiable and M3.4 has no target.
**Effort:** 2 days. **Blocks:** M3.4.

### M3.1 — Multi-environment runtime
N environments in one process, each with its own namespace, authority, limits,
and lifetime. Depends on M1.2 (a `Store` field) and M1.3 (authority below the
interfaces — without them, environment A has no way to be prevented from reaching B).

### M3.2 — Environment descriptor
Extend the readiness payload into the canonical versioned environment descriptor.
**This is the cheapest high-value item in M3** because the payload already exists
and all three implementations already agree field-for-field
(`internal/ready/ready.go`, `ready.ts`, `ready.py`). Additions identified in
`stow-environment.md` §3.8: `interfaces[]`, nested `limits{}`, `rangeReads`,
`checksums`, and `mode` → `profile`.

### M3.3 — Profile normalization across surfaces
Make the two clients select the same backend for the same session contract
(§3 of the plan's "already broken"). Until this holds, M2.0's cross-runtime
suites are testing two different systems.

### M3.4 — Density and churn benchmarks in CI
`T_create`, `T_destroy`, `M_env` at 100/1,000/10,000. Churn: create → PUT×10 →
GET×10 → destroy, ×10,000. Leakage after 10,000 cycles: **0** leaked
environments, credentials, temp directories, processes, multipart state, upstream
claims.
**Done when:** these are CI gates, and the "0 leaked" assertion is a test, not a
comment.

### M3.5 — Optimize creation and disposal
Only after M3.0. Suspected targets, unverified: the double body copy
(`storage/util.go` `ETagForReader` is `io.ReadAll` and never consults
`ByteReader`, so `memory.go` and `fs/fs.go` copy every body twice where
`workspace` does not) and per-request allocation in the S3 hot path.

---

## 7. M4 — Agent State

**Outcome:** the full lifecycle — allocate → work → delegate → extract → destroy.
Correctly ordered: attenuation requires enforcement to exist first.

### M4.1 — Attenuation
Derive a child capability that is never stronger:
`Authority_child ⊆ Authority_parent`, `Quota_child ≤ Quota_parent`,
`Expiry_child ≤ Expiry_parent`. A capability may only weaken. Depends on M1.3
and M1.5.

### M4.2 — Handoff as a real primitive
Today: identity/reference. Target: environment reference + attenuated capability.
Neither `handoff` nor `promote` exists (`grep -rn "func.*[Hh]andoff\|func.*[Pp]romote"`
→ no matches); both are conceptual.

### M4.3 — Promotion as the persistence primitive ⭐
**This is the highest-leverage item in M4, and it is cheaper than it looks.**

Promote is currently blocked (W9) because it has been conceived as an *upstream
push*, and the upstream path has never worked. But **promotion to a durable store
is a local operation** — move the environment's data somewhere durable and
register it. `registry.go` already provides `Register`/`Lookup`/`Forget`.

Defining promote locally delivers the roadmap's best idea — *"everything is
disposable unless explicitly promoted"* — **without touching the 3,646-line
run-through subsystem at all**, and it deletes W9's blocker rather than waiting
on it.

**This should be re-planned before M4.1.** It may be M1-sized, and it is
independently shippable.

### M4.4 — Snapshot / clone / branch
Explicitly last. Not necessary yet.

---

## 8. The track that is not engineering

**W14 — the accounts — appears in none of the fifteen phases.**

The org, npm trusted publishing, the PyPI trusted publisher, the `.npmrc` line
that routes the scope, and a `curl`-verified publication check. §0.7 of
`agent-dx-plan.md` argues the entire wedge is a distribution claim. A
distribution claim that cannot be `pip install`ed is not one.

M3's outcome — "`stow()` feels like `mkdtemp()`" — is worth **nothing** to
someone who cannot install the package. It is also the cheapest possible thing
for a competitor to copy once installed.

**Recommendation: run W14 as a parallel track from day one.** It is blocked on
nobody in this plan, it is not engineering, and it has the longest lead time.
Every other item here can proceed while it is in flight.

---

## 9. Dependency graph

```
W14 (parallel, unblocked) ─────────────────────────────────────────┐
                                                                 │
M1.1 capabilities ──┬─► M1.4 Environment ──┬─► M3.2 descriptor   │
   (delete first)   │                      │                     │
                    └─► M1.6 profiles ─────┴─► M3.3 same-backend │
                                                                 │
M1.2 Store field ───┬─► M3.1 multi-env                           │
                    │                                              │
M1.3 authority ─────┼─► M2.4 upstream wall                       │
   ⚠️ blocks the most│─► M4.1 attenuation                         │
                    │                                              │
M1.5 identity ──────┴─► M4.3 promote ⭐                          │
                                                                 │
M2.0 sandbox suite ──► M2.6 "sandbox" claim                      │
   ▲                                                        │
   ├─ M2.1 namespace ──┐                                   │
   ├─ M2.2 filesystem ─┤                                   │
   ├─ M2.3 quotas ─────┼─► M3.1 multi-env ──► M3.0 ──► M3.5  │
   └─ M2.5 lifetime ───┘        (gated on authority)   baseline  │
                                                              │
                                            M3.4 density ─────┘
```

**Critical path:** M1.1 → M1.3 → M2.0/M2.1–M2.3 → M3.1 → M3.0 → M3.5.
M1.3 is the highest-leverage item: it blocks the most and it is the only one
fixing a live hole rather than adding structure.

**Two things that look like the critical path and are not:** M4.3 (promote) is
independent and, per §7, may be much smaller than the roadmap assumes. W14 is
independent of all of it.

---

## 10. Verification gates per milestone

| Milestone | Gate | Already available |
|---|---|---|
| M1 | `make standards` + a test asserting native and S3 refuse the same operations | `check-generated`, `check-dry`, ratchets all in `make standards` |
| M2 | Sandbox suite green under `-race`; fuzz targets in CI | `-race` already in CI; **no fuzzing exists yet** |
| M3 | Density and churn benchmarks as CI gates; "0 leaked" as an assertion | **nothing exists** — build M3.0 first |
| M4 | Attenuation properties as unit tests: child ⊆ parent, always | — |

Ratchet baselines live in `scripts/baselines/`. New gates need entries there
(`dry.json`, `file-size.json`, `go-coverage.json`, `go-quality.json`,
`lint-ratchet.json`, `type-escapes.json`).

Note `go-coverage.json` is a **floor** (64.4%), not a spread. Per-package numbers
for the shipped entry points are much lower: `cmd/stow-s3` 40.9%,
`internal/s3api` 55.9%. Judge new work per package, not against the aggregate.

---

## 11. What this plan will not do

- **No new architecture documents.** `stow-environment.md` is the primitive
  document. This plan is its execution. A third document restating either is drift.
- **No rewrite.** Every milestone is expressed as deletions, delegations, and
  tests against existing code. The largest single change is M1.3, and it is one
  package.
- **No new S3 surface.** `docs/compat-contract.md` owns that. Issue #11 is a
  *correctness* fix inside the advertised surface, not an extension.
- **No public "sandbox" claim** until M2.0 is green.
- **No W14-shaped work** in the engineering milestones. It is a separate track
  precisely because it is not engineering.

---

## 12. The first three days

Concrete, and deliberately not a document:

1. **M1.1** — delete `cmd/stow-s3/main.go:70-77`, read `runtime.Capabilities`,
   add the test that the readiness payload agrees with the runtime. *Half a day,
   and every later item depends on it.*
2. **M1.7a** — add the checksum case to `backend_contract_test.go`. Confirm it is
   **red**. Then fix `memory.go` and `fs/fs.go`. *One day, and it closes a
   shipping integrity hole.*
3. **M1.3 design** — write down the `Operation` enum and the `Allows` predicate
   signature, and enumerate every `internal/runtime` method that mutates state.
   That list is the actual scope of the authorization work, and it is currently
   unknown. *Half a day, and it converts the highest-severity item from an
   estimate into a plan.*
