---
status: accepted
---

# A permission that is not checked is not a permission

This ADR records that milestone M1.3 was closed on incomplete acceptance
criteria, reopens it, and decides two questions it left open: what to do about
the four `authority.Operation` values that are defined, exported, and consulted
nowhere, and whether `pkg/stow` keeps a pluggable-store seam that has no
implementor.

It does not change ADR 0002, ADR 0003, or ADR 0005. It makes ADR 0005's consent
boundary enforceable, which is the thing ADR 0005 assumed and the code did not
deliver.

## Context

Commit `25e958c` is titled *"M1.3: authority, enforced below every interface."*
M1.3's own acceptance criteria were four, and three do not hold:

| Criterion | State |
|---|---|
| `grep -riE "authoriz" internal/runtime/*.go` returns matches | Holds weakly — the only hit is a comment |
| An operation refused for a given `Authority` is refused identically through S3, native, and WASM | **Fails** — four of twelve operations are refused by no path |
| `DevBypass` cannot widen authority | **Vacuous** — no principal→authority mapping exists for it to widen |
| A test asserts the native path refuses what the S3 path refuses | Partial — two hand-kept copies of the matrix, agreeing by discipline |

`internal/authority` is good work and is not the problem: a closed twelve-value
operation set, a `uint32` mask chosen so authorization is not the most expensive
line in the hot path, fail-closed behaviour on an unknown operation, `All()`
derived from the list so adding an operation cannot silently narrow it, and the
attenuation rule expressed once. The problem is that four of the twelve values it
defines are never passed to `Allows`.

`EnvironmentDestroy` is the one that matters most. `internal/runtime/instance.go`
carries a comment stating that destruction "*is gated*".
`pkg/stow/workspace.go` calls `w.store.Destroy()` with no check. `ReadWrite()`
withholds `environment.destroy` — asserted by its own test — and the directory is
removed anyway.

`UpstreamWrite` matters second. `pkg/stow/authority.go` documents `ReadWrite()`
as "*an Authority for local work with no reach outside the environment: no
upstream*." The only gate on upstream propagation is
`internal/runthrough/adapter.go`'s `decideUpstreamWrite`, which collapses
Policy × AllowLiveWrites and cannot see the caller's grant. A caller who passes
`ReadWrite()` still gets local writes mirrored to a real bucket.

Two things make this a recurrence rather than an oversight. The commit before
`25e958c` is `1982104`, *"a multipart capability that was decorative."* And the
reason the parallel mechanism exists is structural, not careless:
`cmd/stow-s3/runtime_store.go` wraps the run-through adapter *inside* the runtime
instance, so the adapter sits below the chokepoint and cannot consult the
authority without a dependency cycle. The real gate is unreachable from where the
decision is made.

Nothing caught it because the gate that reports on this code has no test.
`tools/quality` produces every complexity, parameter, and `any` finding and
therefore decides what lands in `scripts/baselines/go-quality.json`; it has no
test file. `node --test scripts/*.test.mjs` matches one file against eight gates.
A milestone whose criteria are prose is not a gate.

## Decisions

### 1. A permission with no enforcement site is deleted, not documented as pending

Three of the four are enforced immediately: `EnvironmentDestroy`, `UpstreamRead`,
`UpstreamWrite`. `EnvironmentPromote` is **removed** from the operation set, from
`pkg/stow`'s re-exports, and from the `ReadOnly` and `ReadWrite` presets, and is
re-added by M4.3 with the operation it guards.

This is a deliberate departure from enforcing all four. Gating
`EnvironmentPromote` before `Promote` exists means asserting that a bit is
consulted by an operation with no call site — which is the exact shape of the
defect this ADR exists to close. ADR 0009 section 4 already records that
promotion's contract is decided and its implementation is blocked; the operation
set should match that state rather than anticipate it.

### 2. The authority is carried to where the decision is made

The adapter gains the `Authority` as a value at construction, so it can be
consulted without a cycle. `UpstreamRead` and `UpstreamWrite` become the single
upstream gate, and `Policy × AllowLiveWrites` becomes attenuation applied when the
instance's authority is built rather than a second, independent mechanism.
`RetryPending` is inside the gate: it currently bypasses even
`decideUpstreamWrite` and runs from the per-second retry worker and from the
admin route, both outside the runtime instance.

Configuration alone never manufactures authority. That is ADR 0005 and ADR 0002
invariant 5; this ADR only makes it reachable.

### 3. The anti-recurrence test is part of the definition

A test asserts that **every** operation in `authority.Defined()` is either
enforced at a chokepoint or documented as deliberately ungated, with the
justification at the definition. Adding a decorative permission fails the suite.

This is the actual fix. The four missing enforcement sites are a symptom; a
permission set that can grow without anyone noticing is the disease. The
refusal matrix becomes one table driving both the runtime and S3 test suites,
which are today two hand-maintained copies.

### 4. `pkg/stow` has no pluggable store

`Options.Store`, `stow.Store`, and `storeAdapter` are removed, with roughly 200
lines of `pkg/stow/store.go`. The public API keeps `Open(Options{…})` and
`OpenWorkspace`.

The seam has no implementor. Its only production consumer is the wasm bridge,
where a JSON-decoded `stow.Options` cannot populate a Go interface field — so the
one surface that would want a pluggable store cannot reach it. And the adapter is
lossy where the internal one is faithful: it forwards two of six `PutOptions`
fields, so conditional writes and checksums are inexpressible through the public
embedded API while the S3 surface has 370 lines of tests for them. It answers
`HeadBucket` by scanning every bucket, because the public interface has no
`HeadBucket` — a cost imposed by a missing method rather than by the operation.

Keeping a pluggable store is a reasonable thing to want. It is not reasonable to
want it *and* ship it unimplemented, lossy, and quadratic on a live route.

### 5. Removing the seam is a breaking change and is versioned as one

Conditional writes, checksums, `Delimiter`, `StartAfter`, and O(1) `HeadBucket`
become unreachable through `pkg/stow` rather than silently absent. A caller
depending on any of them gets a compile error, which is the correct outcome, but
it is a major-version change for the module and the changelog says so.

### 6. The verification substrate is a prerequisite, not a follow-up

Testing `tools/quality` and the seven untested gates lands before M1.3 reopens.
The suppression-detection regex in `check-lint-ratchet.mjs` is deleted rather
than repaired, because it cannot match a multi-line `eslint-disable` and has
therefore been reporting suppressed violations as compliant. The three gates that
maintain separate directory lists consume one shared list, so the code that
decides whether the repository passes is inside the repository's own gates.

## Consequences

- **The milestone record changes.** M1.3 is reopened. Anything citing it as
  complete — M2.4, M3.1, M4.1 all declare it a blocking edge — was blocked on a
  milestone that was not finished.
- **`ReadWrite()` stops lying.** Its documentation becomes true, which is the
  only way a caller can rely on a preset.
- **`EnvironmentPromote` disappears from the public surface** until M4.3. Any
  code naming `stow.EnvironmentPromote` stops compiling. There is none in this
  repository.
- **The outbox gains a parameter.** `runthrough.NewWithOutbox` takes an
  authority. Every construction site is in this repository.
- **A future promotion must arrive with its gate.** M4.3 cannot land while
  `EnvironmentPromote` is absent and ungated; re-adding the bit is part of
  shipping the operation, not a follow-up.
- **The gate suite becomes load-bearing.** Until M0 lands, no milestone's
  acceptance criteria are mechanical, which is the condition that let this
  happen.
- **The plan of record moves.** `docs/architecture/environment-plan.md` is
  superseded by `docs/architecture/environment-implementation.md`, which absorbs
  it and adds requirement identifiers, a traceability matrix, an assumptions
  register, and a binding decision rule.
