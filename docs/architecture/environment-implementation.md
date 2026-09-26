# Stow Environment — Implementation Specification

**Status:** active
**Date:** 2026-09-26
**Baseline:** `main` at `1982104`
**Governs:** the implementation of
[`stow-environment.md`](stow-environment.md) §3 dimensions and §10 invariants
**Acceptance surface:** [`docs/compat-contract.md`](../compat-contract.md) for
everything observable over S3

Normative keywords MUST, MUST NOT, SHOULD, and MAY are used as in RFC 2119.
Every requirement in §3 carries an identifier (`R-*`) and appears in the
traceability matrix in §5 with its implementing work item and its verifying
test. A requirement with no verifying test is not a requirement; it is a wish.

This document replaces `environment-plan.md`, which it absorbed. Section 0.2.1
records what changed in the absorption and what was wrong in the sources.

---

## 0. Before reading further

### 0.0 Progress

Seven items are delivered. Each is a separate commit, each was confirmed red
before it was fixed, and each carries its own `CHANGELOG` entry. `make standards`
is green at `82063ec`.

| Item | Requirement | Commit | What changed |
|---|---|---|---|
| C5.3 | R-302, R-303, R-304 | `eacbc8f` | One-part completion ETag, emptiness guard; rule shared by all three backends |
| C1.1 | R-301 | `7ddb37d`, corrected in `82908b4` | `DeleteObjects` returned the complement; interface now specifies the return |
| C1.2 | R-305 | `a59cce1` | `x-amz-copy-source` is now percent-decoded, split before decode |
| C1.4 | R-306 | `6919ed7` | An over-long range end is clamped; start-past-end still `416` |
| C1.5 | R-307 | `48f444f` | The discarded `ListParts` error no longer disables the part-size check |
| C6.1 | R-1005 | `82063ec` | A read-only authority can open a workspace, and the grant is not consulted to issue it |
| C6.5 | R-1009 | `765efbd` | The live gate is enforced by default, with a recorded opt-out |

Two of these were not in this document's plan as written, and both are worth
recording because the plan was wrong:

- **C5.3 said to delete the `len(parts) > 1` conditional.** That would have made
  the one correct backend match the two incorrect ones. Two of three agreeing
  read like a majority; the compat contract is what settled it.
- **C1.5's defect was found while fixing C1.4**, in the same file, three lines
  from the range parse. It is listed in §4 as `C1.5` but was not found by the
  review that wrote this document.

Still pending, in the §6.1 order: **M0.1** (test `tools/quality` and the seven
untested JS gates), **M0.2**, **M0.3**, **M0.4**, **C2.1**, and ADR 0010
decision 2.

### 0.0.1 What landed underneath this document

These items were written from a review of `1982104`. PR #24
(`5ddd88d`, "One capability, one answer") landed while the work above was in
progress and changed the ground under three of them. Recorded here because a
specification that does not notice a refactor underneath it is the failure mode
this document exists to prevent.

**Multipart is now an optional interface, and the duplication is gone.**
`internal/storage.Store` was one interface with all eight multipart methods
inline; `Options.DisableMultipart` let a constructor declare a capability the
runtime then gated on; and `pkg/stow` re-derived it by asserting eight times.
All of that is deleted. `internal/runtime` and `internal/s3api` now ask the store
once. This closes the "capability duplication" row §2 previously listed as a live
finding.

**`R-203` is narrowed rather than closed** — see the requirement.

**`internal/s3api/validators.go` lost `validBucketName`** in favour of
`storage.ValidBucketName`, and `s3api.Server` holds multipart as
`s.multipart storage.MultipartStore` rather than reaching through `s.store`.

The interface split is also the reason one of the tests above had to be rewritten
rather than rebased. `unreadablePartsStore` embedded `storage.Store` and
overrode `ListParts`; after the split those methods belong to
`storage.MultipartStore`, so the stub silently stopped intercepting anything and
the suite stayed green while the test tested nothing. See the commit that fixes
it — it is the clearest argument in this repository for M0.1.

### 0.1 What this document is

An implementation specification, not a proposal and not a review. It states
required end states, the work that establishes them, and the test that proves
each one. It does not argue for them.

Where a decision was made and the reasoning recorded, this document points at
the ADR rather than restating it (§0.3). Where the architecture is described,
`stow-environment.md` owns that. This document owns only: what must be true, in
what order, and how it is verified.

**The scope boundary is deliberate.** `stow-environment.md` states *what Stow
is*. This document states *what must change in this repository to make it true*.
Most of §3 is therefore about removing things: a permission that is checked
nowhere, a decision written down three times, an interface that forces its own
consumers to recover its capabilities at runtime.

### 0.2 What was verified, and what was not

Verified by reading or executing at `1982104`, on 2026-09-26:

- Four of twelve `authority.Operation` values have zero enforcement call sites
  (`comm` over the definitions in `internal/authority/authority.go:28-52` against
  every `check(authority.X)`). Enforced: `BucketCreate`, `BucketDelete`,
  `BucketList`, `ObjectRead`, `ObjectWrite`, `ObjectDelete`, `ObjectList`,
  `EnvironmentReset`. Not enforced: `EnvironmentDestroy`, `EnvironmentPromote`,
  `UpstreamRead`, `UpstreamWrite`.
- `internal/storage/workspace/objects.go:183-200` and
  `internal/storage/memory.go:251-261` return exact complements from
  `DeleteObjects`; `internal/storage/fs/fs.go:362` agrees with memory.
- `internal/s3api/validators.go:87-92` contains no percent-decoding. A repository
  grep for `QueryUnescape|PathUnescape` in `internal/s3api` returns four hits, all
  in `router.go:31,38,53,59`.
- `internal/s3api/handlers_bucket.go:135-160` and
  `internal/s3api/handlers_multipart.go:52-77` are byte-identical over 26 lines
  (`diff` reports no differences).
- `grep -rn "func Fuzz"` returns 0 matches. No fuzzing exists in the repository.
- `node --test scripts/*.test.mjs` matches one file; `scripts/` contains eight
  `check-*.mjs` gates. `tools/quality` has no `_test.go` file.
- Per-package coverage re-measured: `cmd/stow-s3` 41.7%, `internal/s3api` 58.7%,
  `pkg/stow` 55.1%. The `go-coverage.json` floor is 64.4% aggregate.
- A read-only authority **cannot open a workspace.** `pkg/stow/workspace.go:133`
  bootstraps the workspace bucket through the environment it is about to hand out,
  and `ReadOnly` withholds `bucket.create` (`pkg/stow/authority.go:50`), so
  `OpenWorkspace(&ReadOnly())` fails at the constructor. The doc comment at `:61`
  states that passing `&stow.ReadOnly()` "hands out a workspace an agent can read
  but not change."
- `Registry.Collect` (`internal/storage/workspace/registry.go:180`) has exactly
  one production caller, `pkg/stow/resume.go:107`. No server sweep, no CLI entry
  point, and neither SDK client reaches it.
- Checksum verification is already pinned by the shared contract:
  `TestStoreRejectsABodyThatContradictsItsChecksum`
  (`internal/storage/backend_contract_test.go:358`), added because memory and
  filesystem "stored the algorithm and value verbatim." R-302 does **not** need to
  add it.
- **Thirteen test files are outside TypeScript type checking entirely.** Found on
  2026-09-26 while doing M0.3, by noticing that the four gates disagree about
  where the TypeScript is. `packages/stow-s3/tsconfig.json` includes
  `src/**/*.ts` and nothing else, and `npm test` runs the suite through
  `tsx --test`, which strips types without checking them. Verified rather than
  inferred: `const wrong: number = "not a number"` in a test file passes
  `tsc --noEmit` with no diagnostic. So `strict` and `noUncheckedIndexedAccess`
  protect the shipped code and not the code that tests it, while the lint ratchet
  counts findings across the whole package and the type-escape gate scans `src`
  and `test` — three gates, three different notions of the same fact.

  Adding a `tsconfig.test.json` that includes `test/**/*.ts` surfaces four
  diagnostics in two files, and both are real rather than noise:

  - `test/ownership.test.ts:11` imports a `.ts` extension, which `tsx` allows and
    `tsc` does not without `allowImportingTsExtensions`. Mechanical.
  - `test/lifecycle.test.ts:317,324,332` passes `accessKeyId`, `secretAccessKey`
    **and** a `provider` to `buildAwsSdkV3Config` and
    `Stow.awsSdkV3Config`, and asserts that the provider wins. The public type
    `AwsSdkV3ConfigOptions` forbade exactly that combination with
    `provider?: never` and `accessKeyId?: never`, while `src/instance.ts:27`
    resolves the conflict in favour of the provider and a test depends on that
    precedence. **The type is wrong, not the test**: behaviour the implementation
    performs and its tests assert was unreachable from the public type.

  This is why M0.3 is not simply "one SCAN_ROOTS". Unifying the roots is the
  cheap half; the expensive half is that doing it turns on a gate that has been
  reporting success over thirteen files it never read. Widening
  `AwsSdkV3ConfigOptions` to admit the combination is an API decision, because the
  union is what currently lets `buildAwsSdkV3Config` narrow to non-optional
  credentials in its else branch — widening it to a flat type pushes the "neither
  supplied" case into the implementation, where it currently produces an object
  with `undefined` credentials for the SDK to reject later. That needs a decision
  about what a caller with no credentials should get, and it does not belong in a
  commit about root lists.

**Not verified, and therefore not claimed:**

- End-to-end upstream propagation. The local wiring is present
  (`internal/runthrough/adapter.go:240-272` reaches
  `enqueuePreparedIntentLocked` at `:258` under `decideUpstreamWrite`, and
  `internal/runthrough/adapter_test.go:305` covers queueing a failed mirror
  write), but `conformance/upstream_test.go:25` skips unless
  `STOW_CONFORMANCE_UPSTREAM=1` and does not run in CI. **A mirror-writes write
  reaching a real provider is untested.**
- Real-S3 fidelity of the divergences in R-307 and R-308. The divergences are
  read from RFC 9110 and AWS documentation; the repository's live suite that
  would confirm them never runs.
- `internal/runthrough`'s PID-reuse wedge (R-704) is traced statically, not
  demonstrated. Reproducing it requires two processes and a recycled PID.
- Whether the sixteen functions in `conformance/local_test.go` map onto the
  fifteen cases in `conformance/corpus/cases.json`. Unmapped; R-902 depends on it.
- Gate skippability in `.github/workflows/release.yml`. `ci.yml` was read: SHA
  pins, `concurrency`, `timeout-minutes`, no `if:` or `continue-on-error`. The
  release workflow was not read.

### 0.2.1 Corrections kept

Consistent with `stow-environment.md` §0.2.1, corrections are recorded rather
than quietly applied. Two of these were wrong in documents this one supersedes,
and one is wrong in a document this one does not supersede.

| Source claimed | Actually | Resolution |
|---|---|---|
| `environment-plan.md` §3: "Only 1 of 3 stores verifies checksums" — marked *Shipping* | All three call `verifyChecksum`. Fixed by `6b39462` | Struck from the defect table. **Do not re-fix.** R-303 is the missing *contract case*, not a missing check |
| `environment-plan.md` §3: "Read-through never reads through" — "23% of production Go is non-functional" | `internal/runthrough/adapter.go:294` `resolveObject` has a correct upstream fallback. Fixed by `e36ba3d` | Struck. **Do not re-fix** |
| `stow-environment.md` §0.2: "Read-through does not read through" (measured 2026-09-26) | Same as above — fixed | **§0.2 and §21.2 of that document are now stale and MUST be corrected.** Filed as R-001 |
| `stow-environment.md` §0.2: "A mirror-writes write never reaches the outbox" | Local wiring is present (`adapter.go:240-272`); queueing is tested (`adapter_test.go:305`). End-to-end remains unverified — see §0.2 | Partially stale. Correct the claim; **do not** upgrade it to "verified" — R-702 |
| `environment-plan.md` M1.1 implied complete | `cmd/stow-s3/main.go:75` derives `Persistent` from the runtime, but `:76 Multipart: true` is a literal and `:77 Upstream:` is a string comparison. Two of three fields unmet | M1.1 is 🟡 partial. R-102 |
| `stow-environment.md` M3.2 premise: all three readiness implementations "already agree field-for-field" | Four parsers exist (`ready.go`, `ready.ts:71`, `ready-reader.ts:74`, `ready.py:88`); the Go, TS, and Python corpora cover disjoint cases; `check-version.mjs` gates `SESSION_GOGC` and `STOW_OWNER_MARKER` but not `READY_PROTOCOL_VERSION` | Premise rejected. R-803 |
| `stow-environment.md` M3.5: double body copy listed as "suspected target, unverified" | Confirmed. `internal/storage/util.go:116` `ETagForReader` does `io.ReadAll`; `internal/storage/bytes.go:11-13` claims every store routes through it | Promoted to a requirement. R-903 |
| `stow-environment.md` M1.1-M7 completed 2026-09-26 | M1.3 closed with three of four criteria failing, one vacuously (no principal→authority translator exists) | Milestone reopened. R-001, R-201 |

**The rule, restated because it governs this document:** a line number is not a
claim, and neither is a method name or a commit subject. Read the line, run the
code, or mark it unverified. Two of the eight rows above are a grep hit and a
plausible inference respectively, reported as findings — the same two mistakes
`stow-environment.md` §0.2.1 records about itself.

### 0.3 Where the decisions already live

This document does not decide these. It points at them.

| Topic | Decided by |
|---|---|
| Local authority; policy is not consent | [ADR 0002](../adr/0002-sdk-compatibility-and-mirror-writes.md), narrowed by [ADR 0005](../adr/0005-live-write-requires-explicit-consent.md) |
| A permission with no enforcement site is not a permission; `EnvironmentPromote` is deferred; `pkg/stow` has no pluggable store | [ADR 0010](../adr/0010-an-enforcement-site-or-not-a-permission.md) |
| Lifetime; the workspace's exception to it | [ADR 0009](../adr/0009-workspace-outlives-process.md) |
| Workspace as the default | [ADR 0007](../adr/0007-workspace-is-the-default.md), [ADR 0008](../adr/0008-workspace-backend-real-files.md) |
| Sessions as capability issuers | [ADR 0004](../adr/0004-agent-session-contract.md) |
| The embedded runtime is primary | [ADR 0003](../adr/0003-embedded-runtime.md) |
| The S3 wire surface | [`docs/compat-contract.md`](../compat-contract.md) |
| Cross-runtime agreement | [`docs/workspace-contract.md`](../workspace-contract.md) §8 |
| Eight dimensions, ten architectural invariants | [`stow-environment.md`](stow-environment.md) §3, §10 |

Read the ADR when the question is *what was decided and why*. Read
`stow-environment.md` when the question is *what the pieces are*. Read this
document when the question is *what must change, and how will we know*.

### 0.4 Status legend

Derived by re-tracing, never from a commit subject (§0.2.1).

| Mark | Meaning |
|---|---|
| ✅ | Done when is satisfied. Verified. |
| 🟡 | Work landed; the item's Done when is **not** satisfied. Gap stated inline. |
| ⬜ | No code. |
| 🔴 | Open defect shipping to clients or to a real bucket |

---

## 1. Scope

**In scope.** Every change required to satisfy §3, in the order §6 requires.

**Out of scope.** Production S3 parity; production durability guarantees
(`docs/remediation-plan.md` §3). New S3 surface (`docs/compat-contract.md` owns
it; R-306 and R-308 are conformance corrections inside the advertised surface,
not extensions). Any public API break beyond the single one
[ADR 0010](../adr/0010-an-enforcement-site-or-not-a-permission.md) decision 5
authorises. Rewriting
`internal/runthrough` — see §1.1.

### 1.1 A correction about `internal/runthrough`

The package contains three files whose names suggest wrapper layers —
`outbox_adapter.go`, `mutations_adapter.go`, `multipart_adapter.go`. **They are
Go file splits of a single type.** `*Adapter` is the only `storage.Store`
implementation in the package (`grep -rn "var _ storage.Store" internal/runthrough`
→ no matches; the assertions live in `pkg/stow/store.go:110`,
`internal/runtime/adapter.go:19`, `cmd/stow-s3/runtime_store.go:93`). There is no
chain to collapse and no layering to invert. The real defects in this package are
in the outbox, and they are R-7xx.

Recorded because "find the wrapper chain" is the obvious first move here and it
is a dead end.

---

## 2. Baseline

Current state at `1982104`, stated as checkable facts. Nothing here is a
judgement; §3 is where judgement lives.

| Property | Value | How to re-check |
|---|---|---|
| Go statements | 30,720 across 15 packages | `find . -name '*.go' -not -path '*/node_modules/*' \| xargs wc -l` |
| Largest production file | `internal/storage/memory.go`, 499 lines | `wc -l` |
| Files over the 500-line gate | 1, and it is a test file | `node scripts/check-file-size.mjs` |
| `storage.Store` methods | **12** (4 bucket, 7 object, `Close`) — was 20 before #24 split multipart out | `awk '/^type Store interface/,/^}/' internal/storage/store.go \| grep -cE "^\t[A-Za-z0-9]+\("` |
| `storage.MultipartStore` methods | 8, optional and asserted once per consumer | `awk '/^type MultipartStore interface/,/^}/' internal/storage/store.go` |
| `storage.Store` implementations | 5 declared; 3 are identity forwarders | `grep -rn "var _ storage.Store"` plus `S3Client`/`runthrough` |
| `pkg/stow` runtime type assertions on store capability | **0** — #24 deleted all 8 | `grep -c "a.store.(MultipartStore)" pkg/stow/store.go` |
| `outbox` capability interfaces | 6, with 2 implementations | `outbox.go:61-87`, `outbox_claims.go:34-52` |
| Authority enforcement sites | 21, covering 8 of 12 operations | `grep -rhoE "check\(authority\.[A-Za-z]+"` |
| Baselined ratchet entries | `go-quality.json` 16, `file-size.json` 1; all others 0 | `scripts/baselines/*.json` |
| Fuzz targets | 0 | `grep -rn "func Fuzz"` |
| Gates under `scripts/` | 8, of which 1 has a test | `ls scripts/check-*.mjs`, `ls scripts/*.test.mjs` |
| Live-provider gate | Exists and is wired into the release's `needs`; **skips by default** and a skip still releases | `grep -n live_gate .github/workflows/release.yml`, `grep -n require_configured .github/workflows/live.yml` |
| Collection triggers | 1 of 3 — `pkg/stow.Collect` only; no server sweep, no CLI, no SDK hook | `grep -rn "\.Collect(" --include=*.go` |
| Read-only workspace | **Cannot open** — `pkg/stow/workspace.go:133` bootstraps the bucket through the grant it issues | `OpenWorkspace(&ReadOnly())` returns `operation "bucket.create" is not permitted` |
| Tests skipped conditionally | 7, all legitimately conditional | `grep -rn "t.Skip" --include=*_test.go` |

---

## 3. Requirements

Each requirement is testable as written. `MUST` items that name a file and a
behaviour name the test that proves them in §5.

### 3.1 Authority

**R-101 — Every operation is enforced or documented-ungated.**
`internal/authority` defines twelve `Operation` values. For each, either a
chokepoint consults it, or a named comment at the definition states why not.
Adding an operation with neither MUST fail the suite. This is the
anti-recurrence measure for R-201; without it, a decorative permission is
indistinguishable from an enforced one.

**R-102 — One source of capability truth.**
`cmd/` MUST NOT contain a capability string comparison or a capability literal.
Today `cmd/stow-s3/main.go:76` hardcodes `Multipart: true` and `:77` compares a
mode name. A test MUST assert the announced readiness payload equals the
runtime's own capabilities for every constructor.

**R-103 — Authority is enforced below every interface, identically.**
For every `Operation` and every `Authority`, a refusal MUST produce the same
refusal whether the caller arrived over S3, in-process, or through WASM. The
refusal matrix MUST be one table driving both
`internal/runtime/authority_test.go` and `internal/s3api/authority_test.go`,
which today are two hand-kept copies (8 and 5 tests) that agree by discipline.

**R-104 — `EnvironmentDestroy` is gated; `EnvironmentPromote` does not yet exist.**
`pkg/stow/workspace.go:198` calls `w.store.Destroy()` with no
`Authority().Check`. `internal/runtime/instance.go:70` states "*EnvironmentDestroy
… is gated*"; it is not. `stow.ReadWrite()` withholds `environment.destroy`
(asserted by `pkg/stow/authority_test.go:68`) and the directory is removed
anyway. `EnvironmentDestroy` MUST be gated. `EnvironmentPromote` MUST be removed
from the operation set, from `pkg/stow`'s re-exports, and from the `ReadOnly` and
`ReadWrite` presets, and MUST be re-added by M4.3 together with the operation it
guards — per [ADR 0010](../adr/0010-an-enforcement-site-or-not-a-permission.md)
decision 1. Gating a bit whose operation has no call site is the shape of the
defect this requirement closes.

**R-201 — Upstream propagation requires a grant.**
No code path MUST reach an upstream provider without
`Authority.Allows(op) ∧ PolicyAllows(op)`, and configuration alone MUST NOT
manufacture authority (`stow-environment.md` invariant 5). This includes
`RetryPending`, which today bypasses even `decideUpstreamWrite`
(`internal/runthrough/outbox_adapter.go:230`) and runs from the per-second worker
(`cmd/stow-s3/main.go:207`) and the admin route
(`cmd/stow-s3/runtime_store.go:85`), both outside the runtime `Instance`.

**R-202 — The gate is reachable from where it must be enforced.**
`cmd/stow-s3/runtime_store.go:36-53` wraps the run-through adapter *inside* the
runtime instance, so the adapter sits below the chokepoint and cannot consult
the authority without a cycle. That constraint is the reason R-201 is
unimplemented. The authority MUST be reachable from the adapter by construction —
carried as a value at construction — so that the parallel authority-blind
mechanism can be deleted rather than supplemented.

**R-203 — A principal maps to an authority.**
`internal/s3api` MUST translate an authenticated principal into an `Authority`.
It does not: `s3api.Config` never derives one, and `internal/s3api/auth.go:13-14`
`DevBypass` returns `nil` for every request, so every S3 caller receives whatever
authority the runtime was opened with.

**Narrowed by #24, not closed.** `stow-s3 serve --read-only` now narrows the
environment to `authority.ReadOnly()` and is enforced below every interface, so
the S3 surface, an in-process caller and the admin surface are all refused alike,
and the 403 mapping in `s3api/errors.go` is reachable in production for the first
time. That is a reachable policy, not a per-caller one: `runtime.Instance` holds a
single `Authority` for the whole environment and `s3api.AuthFunc` is
`func(*http.Request) error`, so it authenticates without surfacing a principal. A
per-request authority needs a per-request environment, which is M3.1. An operator
can turn authority down and cannot yet vary it per caller, and the difference is
the whole of what remains.

### 3.2 Storage

**R-301 — `DeleteObjects` returns the keys it confirmed.**
`internal/storage/workspace/objects.go:183-200` returned the keys that *survived*
while `memory.go:251-261` and `fs/fs.go:362` returned the keys deleted, and
`internal/runtime/objects.go:188-198` consumes the result as deleted. A `[]string`
return whose meaning is not in the signature is the whole defect, so the interface
now states it.

**The returned set is the request minus the failures, not the request minus the
absences.** S3's `DeleteObjects` reference says a key that is not found is
"returned as deleted", so an absent key is confirmed and appears in `<Deleted>`.
This was got wrong here twice: the original text said the method "MUST return
which keys were removed and which were not", and the first implementation of this
requirement omitted absent keys and pinned that with a test. A backend that omits
them is indistinguishable at the wire from one that deleted them, and a client
detects the difference only by counting.

Quota is released only for keys that existed. `internal/runtime/objects.go:189-197`
decremented `usage.Objects` for every returned key; once absent keys are included
that lets a caller inflate its own quota by deleting keys that never existed.

Two shared contract cases MUST hold on all three backends: deleting
`["exists","absent"]` returns both, in request order; and a failure partway
returns the keys confirmed so far alongside the error.

**R-302 — Backend behaviour is pinned by the shared contract.**
`internal/storage/backend_contract_test.go` MUST cover, for all three backends:
pagination and delimiter (`Delimiter`, `CommonPrefixes`, `ContinuationToken`,
`StartAfter`, `MaxKeys`, `IsTruncated` — currently **zero** occurrences);
conditional writes (`IfMatch`, `IfNoneMatch` — currently **no test anywhere sets
these through a store**); the four multipart divergences listed in R-303; and
`PutObject` against a missing bucket. A conditional-write case MUST assert the
original body is still readable after each refusal, which is the only assertion
that catches a backend checking preconditions *after* overwriting.

**R-303 — Multipart behaviour is identical across backends.**
**Delivered in `eacbc8f`.** This requirement was written with the divergence
backwards, and the error is preserved here because it nearly caused the wrong fix.
It read: "Single-part ETags: memory and filesystem compose (`memory.go:398`,
`fs/fs_multipart.go:136`), workspace emits a bare MD5
(`workspace/multipart.go:214`)" and prescribed that "`CompositeETag` MUST be
applied unconditionally."

The workspace backend was the correct one. A completion with exactly one part
produced a single-part object, and S3 returns that part's own ETag verbatim; the
`-{partCount}` suffix belongs to genuinely multipart objects. Memory and
filesystem were returning `md5(md5(body))-1` where S3 returns `md5(body)`. Two of
three backends agreed with each other and majority agreement read like
correctness, so applying the prescribed fix would have converted the one correct
implementation into two incorrect ones and lost the rule entirely.

What settled it was `docs/compat-contract.md:90`, which this requirement had not
consulted. The lesson generalises past this line: when a specification says two
implementations agree and one differs, the agreement is the suspect.

The rule is now one function, `storage.CompletionETag`, called by all three, and
the previously-correct case is pinned so it cannot regress quietly. Empty
completion is rejected by `ValidateMultipartPartNumbers`, which is total, and the
backend-local checks are gone. Still open under this requirement: a wrong part
ETag, and `ValidateMultipartUpload` with a wrong key, each differ across backends
and are not yet pinned.

**R-304 — Conditional writes are wired.**
`CheckWritePreconditions` is correctly shared
(`internal/storage/preconditions.go:6`) and correctly evaluated at three call
sites — `memory.go:150`, `fs/fs.go:246`, `workspace/objects.go:46`. The helper is
unit-tested; the wiring is not. Deleting all three MUST fail the suite (today it
would not).

### 3.3 S3 fidelity

Corrections to observable behaviour. Each MUST have a conformance case executed
through the real `aws-sdk-go-v2` client, which the repository already uses.

**R-305 — `x-amz-copy-source` is percent-decoded.**
`internal/s3api/validators.go:87-92` splits on `/` and returns the raw segments.
AWS requires the value URL-encoded and SDK callers supply
`url.PathEscape(bucket+"/"+key)`, so a key `my file.txt` arrives as
`my%20file.txt` and is looked up literally. `CopyObject` MUST succeed for keys
containing spaces and non-ASCII characters. `conformance/local_test.go:237`
copies the key `"original"`, which needs no encoding, so the suite cannot detect
this today.

**R-306 — An over-long range end is clamped, not rejected.**
`internal/s3api/handlers_multipart.go:276` returns 416 when `e >= size`. RFC 9110
§14.1.1 and AWS treat a range as unsatisfiable only when the *first*-byte-pos is
at or beyond the length; an over-long last-byte-pos is clamped. `bytes=0-99999`
on a 10 KiB object MUST return 206 with the whole object and
`Content-Range: bytes 0-10239/10240`. `bytes=99999-` MUST still return 416.
`bytes=0-` already works (`parts[1]==""` → `e = size-1` at `:269`), which is why
this survives casual testing. Multi-range requests remain 200; single-range is
the documented scope.

**R-307 — A failed `ListParts` MUST NOT produce success.**
`internal/s3api/handlers_multipart.go:106` discards the error with `_`. An empty
`partSizes` makes the `EntityTooSmall` guard at `:117` unreachable, because `ok`
is always false, so `CompleteMultipartUpload` at `:125` proceeds. A client MUST
NOT receive 200 for an upload with undersized non-final parts.

**R-308 — Capability and version claims are computed once.**
`cmd/stow-s3/main.go:76-78` hand-authors three of five capability literals. The
readiness payload MUST be derived from the runtime's own capabilities and an
explicit S3 feature set, replacing the literals. `internal/ready` already has one
constructor and one caller; the content of that call site is the problem.

**R-309 — The asserted version MUST be a value.**
`packages/stow-s3/test/session.test.ts:104` asserts
`assert.match(capabilities.binaryVersion, /^\d+\.\d+\.\d+$/)` under a comment
claiming it proves the right binary answered. `"0.2.0"` matches exactly as well
as a branch build. The assertion MUST compare against the version in
`package.json`, and a gate test MUST fail if a resolved binary's version
disagrees. `resolveStowBinaryDetailed` MUST be exported so a consumer can ask
which binary was resolved.

### 3.4 Upstream propagation and the outbox

**R-701 — One outbox capability interface.**
Six interfaces describe the outbox capability set (`outbox.go:61-87`,
`outbox_claims.go:34-52`) with two implementations. The gate protecting a real
bucket, `internal/runthrough/adapter.go:187 requireDurableOutbox`, checks
`Durable()` and `CoordinatedOutbox` — **neither implies `ClaimableOutbox` or
`OwnedPreparedOutbox`**. An outbox with no fencing token, no lease, and no claim
passes the gate and then takes the unowned branches:
`outbox_claim_adapter.go:15` returns `acquired: true` with no provider,
`outbox_adapter.go:88` commits unfenced, `:203` marks failure unguarded. There
MUST be one interface, and the fencing operations MUST be part of it rather than
optional.

**R-702 — Ordering and claim decisions MUST NOT be made on stale state.**
`internal/runthrough/file_outbox.go:236-244` `Pending()` cannot return an error,
so on failure it falls back to `o.state.pending()` — and
`completeIntentWithScheduleLocked` and `retryEntry` build their ordering and
claim decisions on that value. `Pending()` and `Prepared()` MUST return errors.
`SnapshotOutbox` exists solely to work around the missing error return and MUST
be deleted with the fix.

**R-703 — A failed batch prepare MUST leave no residue.**
`internal/runthrough/mutations_adapter.go:118-137` returns mid-loop without
rolling back, leaking prepared entries that carry a live 30-second lease.
`RecoverPrepared` then skips them (the owner is our own live PID) and
`rejectPreparedKey` returns `ErrOutboxPreparedUnresolved` for every subsequent
write to **every key in the batch, including the ones that succeeded**.
`outbox_retry_test.go:333` currently asserts the leak. That assertion MUST be
corrected in the same commit as the fix.

**R-704 — A claim lease MUST be the whole recovery mechanism.**
`internal/runthrough/outbox_claims.go:71`:
```go
if entry.ClaimUntil.After(now) || outboxOwnerAlive(entry.ClaimOwner) {
	return ClaimedEntry{}, false, nil
}
```
`ClaimUntil` is the lease; the `||` adds a second, unbounded hold.
`outbox_owner_unix.go:12` returns true for any live PID, and PID reuse is
guaranteed on a developer machine — the normal state, since
`cmd/stow-s3/main.go:318` starts a worker per process. On a recycled PID the
claim is held forever, silently: `acquired: false` → `completeIntentWithScheduleLocked`
returns `nil` with no error, log, or counter → `isFirstPendingForKey`
(`outbox_adapter.go:389`) blocks every later intent for that key, and `Discard`
also refuses (`outbox.go:245`). `ClaimableOutbox.Release` at
`outbox_claims.go:220` is the intended escape and has no caller. Liveness MUST
NOT participate in the hold decision, and `Release` MUST be called on the error
path in `propagationClaim.propagate` (`outbox_claim_adapter.go:75-80`).

**R-705 — A key lock MUST NOT be held across a network round trip.**
`internal/runthrough/adapter.go:249-279` takes a per-key mutex with
`defer unlock()`, so the lock spans the client's full body upload, a cache
delete, durable file writes, and a synchronous upstream PUT. `DeleteObjects` is
worse: `lockMany` over N keys (`mutations_adapter.go:84`) held across the batch
delete and N sequential propagations. The per-second worker contends for the same
lock. The outbox file lock (`file_outbox_lock_unix.go:19`) is
`syscall.Flock(fd, LOCK_EX)` with no `LOCK_NB`, no retry, no deadline, and no
context, so a wedged peer blocks a live request uncancellably. The key lock MUST
be released before propagation; the file lock MUST take a context and a deadline.

**R-706 — One mutation template.**
`PutObject` (`adapter.go:239`), `CopyObject` (`mutations_adapter.go:179`),
`CompleteMultipartUpload` (`:235`), and `DeleteObject` (`:11`) are the same forty
lines, and **they have already drifted**: `CompleteMultipartUpload` is the only
one that omits `invalidateCache`, so completing a multipart upload leaves the
previous object in the separate cache store, where `collectCacheCandidates` counts
a phantom against `STOW_CACHE_MAX_BYTES`/`MAX_OBJECTS` and the stale bytes are
never reclaimed. `outbox_adapter.go:337` discards the `bool` that
`mutations_adapter.go:42` consumes. `invalidateCache` MUST be inside the template
such that a new operation cannot omit it.

**R-707 — Listing MUST NOT enumerate the world per page.**
`internal/runthrough/adapter.go:447 listAllObjects` walks every page of local
**and** upstream on every `ListObjectsV2`, then re-paginates in memory. Page 2 of
a `max-keys=1` listing of a 1M-object bucket re-fetches 1M objects from a real
provider. The same function is on the read path via `collectCacheCandidates`
(`cache_policy.go:103`). Separately, `refreshFromUpstream` (`adapter.go:376-409`)
does an uncapped `io.ReadAll`, and `S3Client.PutObject` (`upstream.go:90`) reads
the payload again, so a 4 GB object OOMs the server and a mirrored PUT is
buffered twice. Merging MUST be bounded to the page window, and the cache fill
MUST stream.

The consistency claim is settled and is **eventual** — see §7.4 and
[`docs/compat-contract.md`](../compat-contract.md) §6.2. Bounding the fetch
weakens no guarantee, because no cross-source guarantee was available to begin
with. No conformance case may assert ordering or completeness across pages; a
case may assert that keys from both sources appear, that a duplicate key resolves
to local metadata, and that a bounded fetch does not re-read the full set.

**R-708 — The outbox state MUST be a sum type.**
`OutboxEntry` (`outbox.go:37-59`) carries six nullable fields and two booleans
with no enum and no transition function; "claimed" is a non-empty string tested
at eight sites. Eight transition functions cover four states with two paths each
(`commit`/`commitPrepared`, `discardPrepared`/`discardPreparedOwned`,
`markSuccess`/`markClaimedSuccess`, `markFailure`/`markClaimedFailure`). The
ordering rule is implemented twice, differently
(`outbox_adapter.go:389 isFirstPendingForKey`, `outbox_claims.go:86 firstForKey`)
and evaluated three times per attempt. `Prepared` is a second copy of which map
an entry lives in, which is why `file_outbox.go:70-81` contains a repair loop
whose following "both prepared and active" guard is unreachable. `entryState`
MUST be a sum type, and the `Prepared` field and its repair loop MUST be deleted.

### 3.5 Durability

**R-801 — One atomic-write implementation with one guarantee.**
Two functions of the same name and intent offer different guarantees.
`internal/storage/fs/fs_atomic.go:40-43` fsyncs the parent directory after
rename but discards both the `os.Open` and `Sync` errors, so a failed sync
returns success. `internal/storage/workspace/manifest.go:123` renames with no
directory sync at all; a rename is not durable until the directory entry is
synced, so every workspace object and manifest write is exposed to power-loss
loss that the filesystem backend survives. A single `internal/atomicfile.Write`
MUST perform temp → fsync → rename → fsync parent and MUST return the errors.

**R-802 — `HEAD` MUST NOT write durably.**
`internal/storage/workspace/objects.go:138` `HeadObject` reaches `resolve`, which
treats a stale or absent entry as adoptable and calls `record` →
`manifest.save()`. The first `HEAD` on a host-written file takes the write lock
and rewrites the entire manifest, which is marshalled whole
(`workspace/manifest.go:110`) on every `PutObject` (`objects.go:89`) — so an agent
that writes 500 files and lists them pays 500 full-manifest rewrites, O(n) each.
Adoption MUST write only the object index, and the identity document MUST NOT be
rewritten per object.

**R-803 — The readiness contract MUST have one fixture.**
Four parsers implement one protocol: `internal/ready/ready.go`,
`packages/stow-s3/src/ready.ts:71`, `ready-reader.ts:74` (a third TypeScript
parser that hand-builds all ten capability fields for the legacy line), and
`packages/stow-s3-py/src/stow_s3/ready.py:88`. The Go, TypeScript, and Python
test corpora cover disjoint case sets — `bool` where a number is required is
Python-only; `42` and `"a string"` are TypeScript-only. One fixture,
`internal/ready/testdata/ready-v1.json`, MUST be generated once by the Go test
and parsed by all three client suites, and `scripts/check-version.mjs` MUST gate
`READY_PROTOCOL_VERSION` rather than only the incidental constants. The
repository already solves the corpus problem one directory over —
`conformance/corpus/cases.json` is read by both Go and TypeScript; reuse that
shape.

**R-804 — An upstream error MUST NOT be persisted verbatim.**
`internal/runthrough/outbox.go:263-274` stores `cause.Error()` into
`entry.LastError`, republished through `internal/s3api/admin.go` as
`last_upstream_error`. AWS `SignatureDoesNotMatch` and `InvalidToken` responses
echo the canonical request and `AWSAccessKeyId`; SDK credential-provider errors
carry file paths. Errors MUST be classified and bounded at the boundary beside
`mapUpstreamError` (`upstream.go:174`), keeping only `Code` and `StatusCode`.
Endpoint redaction is already correct in all three places that print one
(`banner.go:15`, `cmd/stow-s3/main.go:343`, `doctor.go:237`).

### 3.6 Interfaces and layering

**R-901 — `storage.Store` MUST declare the capability split it already relies on.**
The interface is 20 flat methods. The one real split — paginated versus
whole-list `ListParts` — is declared nowhere and discovered by two contradictory
assertions: `internal/runtime/multipart.go:117` takes a silent unpaginated
fallback, `backend_contract_test.go:273` hard-fails. A fourth backend omitting
`ListPartsPage` would compile, pass nothing, and silently lose pagination.
`ListPartsPage` MUST be on the interface and `ListParts` MUST be deleted; then
`ValidateMultipartUpload` MUST be folded into
`CompleteMultipartUpload(ctx, bucket, key, uploadID, parts)`, which currently
requires four pass-through layers to reach the store
(`runthrough/multipart_adapter.go:17`, `pkg/stow/store.go:368`,
`internal/runtime/adapter.go:220`, `internal/runtime/multipart.go:237`) and can be
forgotten by a new caller. Then, and only then, split by capability: `Buckets`,
`Objects`, `Multipart`.

**R-902 — Capability detection MUST NOT be repeated at the call site.**
Because multipart is not separable in the type, `pkg/stow/store.go` recovers it at
runtime eight times — lines 273, 285, 301, 317, 325, 337, 353, 372, each
`multi, ok := a.store.(MultipartStore)`. Detection MUST happen once, at
construction, with the resolved value stored.

**R-903 — `ETagForReader` MUST honour the `ByteReader` contract.**
`internal/storage/bytes.go:11-13` states that every store in the package obtains
body bytes through `ETagForReader`, which is what lets the runtime hand a caller's
buffer to a store without another copy. `internal/storage/util.go:116` does
`io.ReadAll` and never consults `ByteReader`, so `memory.go` and `fs/fs.go` copy
every body twice on the S3 path. `ETagForReader` MUST call `BytesOf`. The
documented invariant and the code MUST agree; one of them is currently false.

**R-904 — The write-commit sequence MUST be one function.**
The same seven steps — validate, resolve bucket, resolve existing, preconditions,
read body, verify checksum, mint version — are sequenced differently per backend.
`memory.go:123-152` consumes the body before resolving the bucket; `fs.go:221-256`
resolves the bucket first. Observable: `PutObject` against a missing bucket
returns `ErrBucketNotFound` from the filesystem backend without draining the
reader and from memory after buffering the entire body.
`workspace/objects.go:41 putLocked` is the same sequence a fourth time. One
`CommitObject` MUST own the order, with backends supplying resolvers and a commit
callback.

**R-905 — `PaginateObjects` MUST own the prefix filter.**
`util.go:256` owns delimiter and continuation semantics but delegates
"pre-filter by prefix and sort by key" to callers, and all three backends
re-implement it: `memory.go:290`, `fs_list.go:66`, `workspace/objects.go:250`.
`fs_list.go` filters on the **decoded** key, the only correct choice given key
sharding, and `longkeys_test.go:191` proves it for the filesystem backend only.
Memory or workspace filtering on the encoded path would be equally correct today
**by accident**.

**R-906 — Admin authorization MUST derive from the route table.**
`internal/s3api/cors.go:123 isDestructiveAdminPath` decides privilege from a
hand-maintained string list that is not the routing table; the routes are declared
at `admin.go:50`; `admin_auth_test.go:118` is a third copy. Adding a seventh
destructive route and forgetting `cors.go` leaves it loopback-reachable with no
credential, while `retry` and `discard` drive writes to a real bucket. Routes,
method, and the `destructive` policy MUST be one table; the test MUST iterate it
and therefore be incapable of disagreeing with the code.

**R-907 — The interface layer MUST NOT import the subsystem beneath it.**
`internal/s3api/admin.go:12` imports `internal/runthrough`, declares eight local
interfaces (`admin.go:16-48`), and asserts them against `s.store` sixteen times —
while all ten methods behind those eight interfaces are on one type,
`*runthrough.Adapter`. The assertion MUST resolve once, at wiring, to one
interface declared by the package that owns the methods, and a misconfigured store
MUST fail at startup rather than silently degrade three endpoints at request time.

**R-908 — `writeStatus` MUST NOT enumerate every object.**
`internal/s3api/admin.go:151` calls `listAllAdminObjects` for every bucket to
compute `object_count`, on the credential-free loopback path
(`server.go:338`). Any local process can trigger an unbounded full enumeration. The
count MUST come from the store, or the field MUST be dropped.

**R-909 — The WASM bridge MUST NOT return an unvalidated result.**
`packages/stow-s3/src/embedded.ts:225-253` validates the envelope — version, ok
flag, error shape — then returns `parsed.result as T` with no inspection, and all
thirteen `EmbeddedStow` methods return through that line. `fromBridgeObject`
(`:259-264`) compounds it by spreading the unvalidated record. A cast to a *type
parameter* is invisible to `check-type-escapes.mjs`, which counts `as any` and
`as unknown as`, so the widest escape in the codebase is the one the escape gate
cannot count. Results MUST be decoded per operation against the Go
`operationHandlers` table (`cmd/stow-wasm/operations.go:14`).

**R-910 — `pkg/stow` MUST NOT expose a pluggable store.**
`Options.Store`, `stow.Store`, and `storeAdapter` MUST be removed — roughly 200
lines of `pkg/stow/store.go` — per [ADR 0010](../adr/0010-an-enforcement-site-or-not-a-permission.md)
decision 4. The public API keeps `Open(Options{…})` and `OpenWorkspace`.

The seam has no implementor: its only production consumer is
`cmd/stow-wasm/main.go:151`, where a JSON-decoded `stow.Options` cannot populate a
Go interface field. It is also lossy where the internal adapter is faithful —
`internal/runtime/adapter.go:100-107` forwards six `PutOptions` fields,
`pkg/stow/store.go:186` forwards two, so conditional writes and checksums are
**inexpressible through the public embedded API** while
`internal/s3api/conditional_test.go` (370 lines) has no counterpart there.
`store.go:218` drops `Delimiter` and `StartAfter`. `store.go:165` answers
`HeadBucket` by scanning every bucket because the public interface has no such
method, which is a cost imposed by a missing method rather than by the operation.

Consequences of removal, per ADR 0010 decision 5: conditional writes, checksums,
`Delimiter`, `StartAfter`, and O(1) `HeadBucket` become unreachable through
`pkg/stow` rather than silently absent. A caller depending on any of them gets a
compile error, which is correct, and it is a major-version change for the module.

**R-911 — `serve` MUST be decomposed.**
`cmd/stow-s3/main.go:232-416` is 185 lines at cyclomatic complexity 44 against a
limit of 15, and the ratchet response was to record the entry in
`scripts/baselines/go-quality.json` rather than fix it. It owns eight jobs. A
`serveOptions` struct with a `bindServeFlags` function and a linear `runServe`
MUST replace it, and the baseline entry MUST be deleted in the same commit.

**R-912 — The bind-wait MUST NOT be a poll, and MUST NOT be copied.**
`cmd/stow-s3/main.go:371` busy-polls `srv.Addr()` on a 10 ms sleep with a 2 s
deadline, while `internal/s3api/server.go:104` already owns `ready chan struct{}`
closed by `readyOnce` at the moment the listener binds (`:156`) and on both
early-return error paths (`:114`, `:128`) — `Shutdown` already selects on it. The
same poll appears at `main.go:371`, `doctor.go:293`,
`conformance/harness_test.go:136`, and `s3api_test.go:79`. `Server` MUST export
`Ready()` and all four sites MUST be deleted.

**R-913 — A failed `Open` MUST NOT take ownership of a caller's store.**
`internal/runtime/adapter.go:53` returns on `initialize` failure without closing
the store, contradicting `pkg/stow/store.go:45` ("*a store is closed exactly once
regardless of who owns it*"). `pkg/stow/workspace.go:110` gets this right on its
error path; the pattern is known and not applied.

**R-914 — Public sentinels MUST cover every error a caller can receive.**
`pkg/stow/runtime.go:159 mapError` omits `ErrClosed`,
`ErrExternalResetUnsupported`, and `ErrMultipartUnsupported`, so callers receive
internal strings with no exported name — for example
`Reset = runtime reset is unsupported for an externally managed store
(*errors.errorString)`, describing a concept absent from the public vocabulary.

### 3.7 Verification substrate

**R-1001 — Every gate MUST have a test that exercises its failure mode.**
`tools/quality/main.go` (273 lines) produces every complexity, parameter, and
`any` finding and therefore decides what lands in `go-quality.json`. It has no
test. `node --test scripts/*.test.mjs` matches one file against eight gates. The
Makefile already states the principle — "*a gate with no test is a gate whose
rules are never exercised in the modes they claim to handle*" — and applies it to
one gate in seven. Each gate MUST assert: a new violation fails; a **removed**
violation is reported stale, forcing a baseline edit (CODE_STANDARDS.md:46); a
malformed or empty baseline is rejected rather than treated as zero debt; an
unchanged tree passes.

**R-1002 — A suppression MUST NOT be able to hide a violation.**
`scripts/check-lint-ratchet.mjs:31` detects suppressions with `[^\n]*?`, which
cannot cross a newline and so never matches a multi-line block directive — a form
ESLint honours. A complexity-21 function suppressed with
`/* eslint-disable\n complexity */` reports `OK (complexity=0)`, exit 0. The
hand-rolled pattern MUST be deleted in favour of ESLint's own directive parsing
(`linterOptions.reportUnusedDisableDirectives` reconciled against
`SourceCode#getAllComments()`), which also removes a double-count where one
suppression was scored twice.

**R-1003 — The gates MUST cover the gates.**
`scripts/check-file-size.mjs:8-18` roots omit `scripts/` and
`packages/stow-s3/scripts`, so a 520-line gate file passes. `check-lint-ratchet.mjs:25`
scopes ESLint to the package, so `scripts/*.mjs` gets no complexity, depth, or
parameter limit. `.jscpd.json` scopes to `["src","test"]`, so `scripts/` gets no
duplication check. One exported `SCAN_ROOTS` MUST be consumed by all three.

**R-1004 — Every milestone's acceptance criteria MUST be mechanically checkable.**
M1.3 was closed with three of four criteria failing and one vacuous, because its
criteria were prose. A milestone's Done when MUST be a test assertion or a gate
exit code.

**R-001 — A documented claim about the code MUST match the code.**
Two of the claims this document corrects (§0.2.1) were *measurements*, presented
with the confidence of measurements, and both had been repaired by the time the
correction was written. `stow-environment.md` §0.2 and §21.2 currently assert, as
measured fact, two defects that `e36ba3d` and `6b39462` fixed. Any document
asserting a property of the tree MUST carry the commit it was measured at and MUST
be re-verified when that commit is superseded. The primitive document's own rule
applies: a line number is not a claim, and neither is a measurement nobody re-ran.

### 3.8 Session lifecycle, handoff, and the shared directory

These requirements come from the product's deployment topology rather than from a
defect found in review. §12 maps them to the end state they serve. Nothing in
this section existed in the plan this document absorbed.

**R-1005 — A read-only authority MUST be able to open a workspace.**
`pkg/stow/workspace.go:61-63` documents that passing `&stow.ReadOnly()` "hands
out a workspace an agent can read but not change." It does not: `:133`
bootstraps the workspace bucket *through the environment it is about to hand
out*, and `ReadOnly` withholds `bucket.create` (`pkg/stow/authority.go:50`), so
the constructor itself fails with `stow: create workspace bucket: operation
"bucket.create" is not permitted`. The one restricted-workspace configuration the
documentation names is the one configuration that cannot exist.

Bucket bootstrap is a *construction* step, not a caller operation, and belongs in
the same category as `initialize()` in `internal/runtime/state.go`. It MUST NOT be
evaluated against the grant being issued. Widening `ReadOnly` to include
`bucket.create` is **not** an acceptable fix: that trades a false documentation
claim for an over-broad grant, and the target requires that a read-only workspace
genuinely cannot mutate.

**R-1006 — Collection MUST have all three triggers.**
`internal/storage/workspace/registry.go:180` implements `Collect` with the right
refusal semantics — liveness is *established*, never guessed, and a workspace
that cannot be proven unused is left alone. `pkg/stow/resume.go:107` exposes it as
a public function. That is **one** trigger, called only by a Go caller who asks.
The end state names three: a server-side sweep, a CLI entry point, and SDK hooks.
The server MUST sweep on an interval; a CLI entry point MUST exist; the
TypeScript and Python clients MUST expose collection so a host can reclaim
without writing Go. Collection MUST remain uncollectable for a live session and
for any directory that is a process's working directory.

**R-1007 — Handoff MUST return a capability URL at a reachable location.**
A presigned URL is two things: a capability (the signature) and a location (it
must resolve for the recipient). A URL naming a sandbox the recipient cannot
reach is a dead end, however correctly it is signed.

Handoff is therefore not the same operation as promotion and MUST be specified
separately. Promotion (M4.3) is a *local* durability move — it is deliberately
independent of the upstream path. Handoff has a reachability obligation that
promotion does not: when the recipient is outside the sandbox, the object MUST be
converged to a location they can reach, and the returned URL MUST name **that**
copy rather than the sandbox-local one.

This is gated on M2.7. Convergence presupposes propagation that works, and today
it does not. Handoff-as-a-URL MUST NOT ship before R-201 and R-707 hold, or it
will hand out signed URLs naming copies that may not exist yet.

Handoff MUST return a capability and never a credential. `ADR 0004` §5 already
decides this — a handoff carries the session ID and the capability to open the
workspace, not the secret key — and `packages/stow-s3/src/session.ts:253` still
returns `AWS_SECRET_ACCESS_KEY` in an environment mapping for the in-process-tree
case. That mapping MUST stay available under its current name; the URL MUST NOT
be derived from it.

**R-1008 — The shared working directory MUST be correct under concurrent access.**
The topology puts the agent's own tools and stow in the same directory: the agent
writes files with its tools, stow adopts them. Three accessors reach that
directory — Go in-process, the WASM host, and the S3 server — and they do not
coordinate.

`internal/storage/workspace` takes a mutex for manifest and object mutation, and
the fast path is correct, but there is no stated property and therefore no test
for it. This MUST become one: adoption, listing, and mutation of a
host-written file MUST be safe under concurrent access from all three accessors,
with no deadlock between the manifest lock and the filesystem, and no data race
under `-race`. The specific hazard already identified is that a read can take the
write path — `HeadObject` reaches `record` and therefore `manifest.save()`
(R-802) — so a read/write deadlock is reachable today and the `-race` suite does
not exercise it.

**R-1009 — The release gate MUST verify against a real provider.**
`.github/workflows/live.yml` implements this and `release.yml:172` calls it, but
the gate **skips by default** and a skip still permits a release. The end state
requires the gate to always verify. `live.yml`'s `require_configured` input
already fails instead of skipping; it is not used, and no release has been
verified against a provider as a result.

A release MUST NOT proceed on a skipped live gate. Failing when no provider is
configured is the correct default for a release job specifically, because a
release is the one place where "we did not check" must be expensive. The
`STOW_CONFORMANCE_UPSTREAM=1` suite is the only thing that can confirm §3.3's two
divergences against a real provider, so this requirement is also the mechanism
that retires A-1.

### 3.9 Standing constraints

These are not work items. They bound every change in this document.

**R-1100 — Fidelity defects are contract breaches.**
Anything tested against stow MUST behave identically against real S3. A
divergence in observable behaviour is a defect in the same class as a data-loss
bug, and is recorded as one. This is why §3.3's two items carry conformance cases
rather than a note, and why §7.4 chose eventual consistency over a stronger claim:
matching the compatibility target outranks exceeding it.

**R-1101 — WASM is a host, not a store.**
There are three stores — memory, filesystem, workspace. `cmd/stow-wasm` calls
`pkg/stow.Open`, and `grep "BackendWasm\|\"wasm\"" internal/runtime` returns no
backend by that name. Documentation MUST NOT describe WASM as a storage backend
or as a fourth store. `stow-environment.md` §0.2.1 already records this
correction; the requirement exists so it stays corrected.

**R-1102 — Non-goals are permanent, not deferred.**
Not production object storage. Not compute. Not a database. Local-first,
zero-account, no metering; runs on a laptop, in CI, and air-gapped. Durability
work in §3.5 exists so an acknowledged write survives a crash on a developer's
machine, not to claim a production guarantee. A change that makes one of these
look closer to production storage is out of scope even if it is technically
appealing.

---

## 4. Work items

Ordered by §6. Effort is a lower bound in days.

### M0 — Trust the gates 🔴 blocks everything

| Item | Requirement | Effort |
|---|---|---|
| M0.1 Test `tools/quality` and the seven untested JS gates | R-1001 | 2–3 |
| M0.2 Delete the suppression regex; use ESLint's directive parsing | R-1002 | <1 |
| M0.3 One `SCAN_ROOTS` for file-size, lint, and duplication. **Blocked**: doing it turns on a gate that finds 4 real type errors in 13 unchecked test files — see §0.2 | R-1003 | 1–2, was <1 |
| M0.4 Anti-recurrence: every `authority.Defined()` operation enforced or documented-ungated | R-101 | <1 |

### M1 — Environment core

| Item | Requirement | Status | Effort |
|---|---|---|---|
| M1.1 One source of capability truth | R-102 | 🟡 `main.go:76,77` unmet | <1 |
| M1.2 `Store` field on `pkg/stow.Options` | R-910 | ✅ with caveat | — |
| M1.3 Authority below every interface | R-101,103,104,202,203 | 🟡 4 of 12 dead | 3–4 |
| M1.4 `Environment` struct, delegating | — | ⬜ | 2–3 |
| M1.5 Identity, credential, authority, upstream-credential | R-201 | ⬜ | 2–3 |
| M1.6 Profiles as compositions | — | ⬜ | 2 |
| M1.7 Two cheap correctness bugs | R-304 | ✅ | — |
| M1.8 Four correctness bugs still shipping | R-301,305,306,307 | 🔴 | 3 |

### M2 — Sandbox core

M2.1, M2.2, and M2.5 are **[audit first]**: audit the existing machinery before
planning against it. `layout.go` already implements escape markers, reserved
names, and union-of-host rules; what it misses is not answerable by reading a
threat list.

| Item | Requirement | Status | Effort |
|---|---|---|---|
| M2.0 Sandbox conformance suite **[audit first]** | — | ⬜ 0 fuzz targets exist | 2–3 skeleton |
| M2.1 Namespace wall **[audit first]** | — | ⬜ | audit |
| M2.2 Filesystem wall **[audit first]** | — | ⬜ symlinks are followed | audit |
| M2.3 Limits adversarially correct (verify, don't design) | R-301 | ⬜ | audit |
| M2.4 Upstream wall | R-201,202,203 | 🔴 target was right; not implemented | 3–4 |
| M2.5 Lifetime wall **[audit first]** | R-104 | ⬜ absent on Windows | audit |
| M2.6 Public "sandbox" claim | — | ⬜ gated on M2.0 | — |
| M2.7 Run-through write-path integrity | R-701…708 | 🔴 | 12–15 |

### M3 — Agentic runtime

| Item | Requirement | Status | Effort |
|---|---|---|---|
| M3.0 Cost baseline **[do first]** | — | ⬜ no density benchmark exists | 2 |
| M3.1 Multi-environment runtime | — | ⬜ | — |
| M3.2 Environment descriptor | R-308,803 | ⬜ premise corrected | 2 |
| M3.3 Profile normalization across surfaces | — | ⬜ `session.ts:117` vs `session.py:253` | — |
| M3.4 Density and churn benchmarks in CI | — | ⬜ | — |
| M3.5 Optimize creation and disposal | R-903 | ⬜ suspicion now confirmed | — |

### M4 — Agent state

| Item | Requirement | Status | Effort |
|---|---|---|---|
| M4.1 Attenuation | — | ⬜ `IsSupersetOf` exists; nothing to attenuate yet | — |
| M4.2 Handoff as a real primitive | — | ⬜ does not exist | — |
| M4.3 Promotion as the persistence primitive ⭐ | R-104 | ⬜ blocked on upstream by design; is local | — |
| M4.4 Snapshot / clone / branch | — | ⬜ explicitly last | — |

M4.3 SHOULD be re-planned before M4.1. It may be M1-sized and is independently
shippable: promotion to a durable store is a **local** operation, and
`internal/storage/workspace/registry.go` already provides
`Register`/`Lookup`/`Forget`. Defining it locally delivers "*everything is
disposable unless explicitly promoted*" without touching the run-through
subsystem.

M4.3 MUST re-add `EnvironmentPromote` to the operation set in the same change
that introduces the operation, per R-104 and ADR 0010 decision 1. A future
promotion cannot land while the bit is absent and ungated; re-adding it is part of
shipping `Promote`, not a follow-up.

### M2.8 — Session lifecycle and the reachable handoff 🔴 NEW

**Outcome:** the session primitive is mint → work → hand off → collect, with
every trigger wired and handoff naming a location the recipient can reach.

Specified in full at §3.8. This is separated from M2.0–M2.7 because those are
walls and a durability story; this is the *product* surface the deployment
topology depends on, and none of it is a defect in existing code — it is
specified behaviour that does not exist yet.

| Item | Requirement | Severity | Effort |
|---|---|---|---|
| C6.1 A read-only authority can open a workspace | R-1005 | 🔴 the documented configuration cannot exist | 1 |
| C6.2 Collection triggers: server sweep, CLI, SDK hooks | R-1006 | 🟠 1 of 3 wired | 2–3 |
| C6.3 Concurrent host + stow access to the shared directory | R-1008 | 🟠 read can take the write path today | 3–4 |
| C6.4 Handoff returns a capability URL at a reachable location | R-1007 | 🔴 gated on M2.7 | 5–7 |
| C6.5 Release fails on a skipped live gate | R-1009 | 🔴 machinery exists, unused | <1 |

`C6.1` is listed under M2.8 rather than M1.3 because it is a constructor bug
rather than an enforcement gap, but it is the cheapest item in this table and
belongs in the first three days: a documented capability that cannot be invoked
is worse than an absent one.

`C6.4` MUST NOT start before M2.7. Convergence presupposes propagation that
works, and it does not today.



Parallel; not on the critical path; ordered by severity. These are not
environment work, and folding them into M1–M4 would dilute the milestone that
owns each concept. They share one shape, given in §4.1.

| Item | Requirement | Severity | Effort |
|---|---|---|---|
| C1.1 `DeleteObjects` reports both sets | R-301 | 🔴 | 1 |
| C1.2 `x-amz-copy-source` percent-decoded | R-305 | 🔴 | <1 |
| C1.3 Authority chokepoint real, parallel mechanism deleted | R-201,202 | 🔴 | 3–4 (in M1.3/M2.4) |
| C1.4 Clamp over-long range ends | R-306 | 🔴 | <1 |
| C1.5 Stop discarding the `ListParts` error | R-307 | 🟠 | <1 |
| C1.6 Delete eleven swallowed durability errors | R-702 | 🟠 | 2 |
| C1.7 Version assertion compares a value | R-309 | 🟠 | 1 |
| C2.1 One `internal/atomicfile` | R-801 | 🟠 | 1 |
| C2.2 Split the workspace manifest | R-802 | 🟠 | 2 |
| C2.3 Key lock MUST NOT span a network round trip | R-705 | 🟠 | 2–3 |
| C2.4 Transactional batch prepare | R-703 | 🟠 | 1 |
| C2.5 Bounded upstream reads and streaming cache fill | R-707 | 🟠 | 2 |
| C3.1 `storage.Store` declares the split | R-901,902 | 🟠 | 3–4 |
| C3.2 One admin route table | R-906,907,908 | 🟠 | 1 |
| C3.3 Fold error mappings into the existing table | — ¹ | 🟠 | 1 |
| C3.4 `requestScope` | — ¹ | 🟠 | 2–3 |
| C3.5 Six outbox interfaces → one | R-701 | 🟠 | 2–3 |
| C3.6 One mutation template | R-706 | 🟠 | 2 |
| C3.7 Delete the `pkg/stow.Store` seam | R-910 | 🟠 breaking | 2 |
| C3.8 Typed WASM results | R-909 | 🔵 | 2 |
| C4.1 Decompose `serve`; delete four bind-waits | R-911,912 | ⛔ | 2 |
| C4.2 Outbox state as a sum type | R-708 | 🟠 | 3–4 |
| C4.3 Remove PID liveness from the hold decision | R-704 | 🔴 | 1 |
| C4.4 `writeStatus` count | R-908 | 🟠 | <1 |
| C4.5 Workspace lifecycle state machine; `Open` ownership | R-913,914 | 🔵 | 1 |
| C4.6 Split `util.go`; fix `ETagForReader` | R-903 | 🔵 | 1 |
| C5.1 One `CommitObject` pipeline | R-904 | 🟠 | 3 |
| C5.2 `PaginateObjects` owns the prefix filter | R-905 | 🟠 | 1 |
| C5.3 Close the contract-test holes | R-302,303,304 | 🟠 | 1 |
| C5.4 Retire the file-size baseline entry | — ² | 🔵 | 1 |

¹ No defect requirement: `C3.3` and `C3.4` are verified by ratchet descent — they
retire the three baselined `any` entries and five baselined `max-params` entries
respectively. The gate failing to report them afterwards **is** the test (§10
rule 5).

² Same: `C5.4` retires the single `file-size.json` entry. Its precondition is
assumption A-2.

Items `C2.3`, `C2.4`, `C2.5`, `C3.5`, `C3.6`, `C4.2`, and `C4.3` are specified in
full under **M2.7**; this table indexes them so the traceability matrix in §5
resolves. They appear here as well as there because they are corrective work with
a severity, and severity is what orders Track C.

#### 4.1 The shape shared by most of Track C

Six requirements above are one defect: **a decision with exactly one correct
answer, written down more than once by hand.** The gate metrics cannot detect
this class, because every copy is individually small, tidy, and passing.

| Decision | Copies | Where they diverge today |
|---|---|---|
| Which admin routes are destructive | 3 | `admin.go:50`, `cors.go:123`, `admin_auth_test.go:118` |
| Storage error → S3 code and status | 2 | `errors.go:121` table; two digest sentinels hand-inlined at two sites |
| `storage.Store` → public surface fidelity | 2 | `runtime/adapter.go:100-107` (6 fields) vs `pkg/stow/store.go:186` (2) |
| The outbox capability set | 6 | `outbox.go:61-87`, `outbox_claims.go:34-52`; the gate checks 2 |
| Readiness payload | 3 languages | `ready.go`, `ready.ts:71`, `ready.py:88` |
| Which operations authority refuses | 2 | `runtime/authority_test.go`, `s3api/authority_test.go` |

Each fix in §4 declares the decision once and derives its consumers. A fix that
adds a check without removing a copy does not satisfy this section.

---

## 5. Traceability

Every requirement in §3 appears here with the defect it closes, the work item that
closes it, and the test that proves it. A requirement absent from this table is a
defect in this document.

Work items **not** in this table fall into two groups.

*Verified by ratchet descent rather than a defect requirement:* `C3.3` and
`C3.4` retire three baselined `any` entries and five baselined `max-params`
entries; `C5.4` retires the single `file-size.json` entry. The gate ceasing to
report them **is** the test (§10 rule 5).

*Architecture and audit work, where the target is not yet known well enough to
state a requirement against:* `M2.1` and `M2.2` are **[audit first]** — their
deliverable may be "one traversal bug" rather than a subsystem. `M4.2` and `M4.3`
depend on `M1.3` and `M2.4` and are explicitly re-plannable; M4.3's target is
stated in §4 but its verification is not yet derivable.

Remaining M1, M2, and M3 items that carry no defect requirement are
`M1.2`, `M1.4`, `M1.6`, `M1.7`, `M2.0`, `M2.3`, `M2.5`, `M2.6`, `M3.0`,
`M3.1`, `M3.3`, `M3.4`, `M3.5`, `M4.1`, `M4.4`. They are capability, coverage,
or architecture work rather than corrections; each is verified by its own Done
when in §4.

| Req | Defect evidence | Work item | Verifying test |
|---|---|---|---|
| R-101 | `comm` over `authority.go:28-52` vs `check(authority.X)` | M0.4, M1.3 | every `Defined()` op enforced or documented |
| R-102 | `main.go:76,77` | M1.1 | readiness payload == runtime capabilities |
| R-103 | two test files, 8 and 5 tests | M1.3 | one table drives both suites |
| R-104 | `pkg/stow/workspace.go:198`; `instance.go:70` | M1.3 | `ReadWrite()` refuses `Destroy` |
| R-201 | `adapter.go:194`; `outbox_adapter.go:230` | M1.5, M2.4, C1.3 | `ReadWrite()` in mirror-writes → 0 upstream calls |
| R-202 | `runtime_store.go:36-53` | M2.4, C1.3 | adapter holds the authority at construction |
| R-203 | `auth.go:13-14`; no mapping exists | M1.3 | principal maps to authority on the S3 path |
| R-301 | `workspace/objects.go:183-200` vs `memory.go:251-261` | M1.8, C1.1 | `DeleteResult{Deleted:["exists"]}` on 3 backends; workspace `Usage()` returns to baseline |
| R-302 | 0 occurrences of pagination terms in the contract test | C5.3 | contract cases for each dimension |
| R-303 | `workspace/multipart.go:214`; `util.go:100` | C5.3 | 4-row table across 3 backends |
| R-304 | `memory.go:150`, `fs.go:246`, `workspace/objects.go:46` | C5.3 | deleting the calls fails the suite |
| R-305 | `validators.go:87-92`; `local_test.go:237` | M1.8, C1.2 | real SDK copies a key with a space |
| R-306 | `handlers_multipart.go:276` | M1.8, C1.4 | `bytes=0-99999` → 206 + `Content-Range`; `bytes=99999-` → 416 |
| R-307 | `handlers_multipart.go:106` | M1.8, C1.5 | failing `ListParts` does not return 200 |
| R-308 | `main.go:76-78` | M3.2 | payload derived, not literal |
| R-309 | `session.test.ts:104` | C1.7 | version equals `package.json` |
| R-701 | `adapter.go:187`; `outbox_claim_adapter.go:15` | M2.7, C3.5 | `legacyDurableOutbox` unrepresentable |
| R-702 | `file_outbox.go:236-244` | M2.7, C1.6 | failing snapshot surfaces |
| R-703 | `mutations_adapter.go:118-137`; `outbox_retry_test.go:333` | M2.7, C2.4 | failed batch leaves zero residue |
| R-704 | `outbox_claims.go:71`; `outbox_owner_unix.go:12` | M2.7, C4.3 | multi-process: wedge unreachable |
| R-705 | `adapter.go:249-279`; `file_outbox_lock_unix.go:19` | M2.7, C2.3 | cancelled context aborts acquisition |
| R-706 | `adapter.go:239`, `mutations_adapter.go:11,179,235` | M2.7, C3.6 | multipart complete invalidates cache |
| R-707 | `adapter.go:447`; `cache_policy.go:103` | M2.7, C2.5 | page 2 does not re-fetch the full set |
| R-708 | `outbox.go:37-59`; 8 transition sites | M2.7, C4.2 | sum type; no `Prepared` repair loop |
| R-801 | `fs_atomic.go:40-43`; `manifest.go:123` | C2.1 | `atomicfile.Write` returns sync errors |
| R-802 | `workspace/objects.go:138` | C2.2 | manifest mtime unchanged by `HEAD` |
| R-803 | 4 parsers; disjoint corpora | M3.2 | one fixture parsed by 3 suites |
| R-804 | `outbox.go:263-274` | M2.7 | persisted error carries no canonical request |
| R-901 | `runtime/multipart.go:117` vs `backend_contract_test.go:273` | C3.1 | `ListPartsPage` on the interface |
| R-902 | 8 assertions in `pkg/stow/store.go` | C3.1; the `pkg/stow` copies are removed by deletion under C3.7 | detection at construction only; zero assertions in `pkg/stow` |
| R-903 | `util.go:116` vs `bytes.go:11-13` | C4.6 | `ByteReader` honoured; copy count 1 |
| R-904 | `memory.go:123-152` vs `fs.go:221-256` | C5.1 | `PutObject` on missing bucket in the contract |
| R-905 | three independent prefix filters | C5.2 | `CommonPrefixes`/`KeyCount` agree on 3 backends |
| R-906 | `cors.go:123` vs `admin.go:50` | C3.2 | test iterates the route table |
| R-907 | `admin.go:12,16-48`; 16 assertions | C3.2 | resolves once at wiring |
| R-908 | `admin.go:151` | C3.2, C4.4 | no full enumeration on the loopback path |
| R-909 | `embedded.ts:225-253` | C3.8 | per-operation decoder; Go rename fails loudly |
| R-910 | `pkg/stow/store.go:186,165,218` | C3.7 | `pkg/stow.Store` and `Options.Store` do not exist; `go doc ./pkg/stow` shows no pluggable store |
| R-911 | `main.go:232-416`, complexity 44 | C4.1 | baseline entry deleted |
| R-912 | four copies of the bind-wait | C4.1 | `Server.Ready()` exported; zero polls |
| R-913 | `internal/runtime/adapter.go:53` | C4.5 | failed `Open` closes the store |
| R-914 | `pkg/stow/runtime.go:159` | C4.5 | every error has a `stow.` sentinel |
| R-1001 | `tools/quality` has no test; 1 of 8 gates tested | M0.1 | four ratchet behaviours per gate |
| R-1002 | `check-lint-ratchet.mjs:31` | M0.2 | multi-line disable is counted |
| R-1003 | `check-file-size.mjs:8-18` | M0.3 | oversized file in `scripts/` fails |
| R-1004 | M1.3 closed on prose criteria | M0.4 | every Done when is an assertion |
| R-1005 | `pkg/stow/workspace.go:133` bootstraps through the grant it issues | C6.1 | `OpenWorkspace(&ReadOnly())` succeeds; `PutObject` refused; `Close`/`Path` work |
| R-1006 | `registry.go:180` exists; only `pkg/stow/resume.go:107` calls it | C6.2 | server sweep, CLI, and both SDK clients reach collection; live and cwd workspaces survive |
| R-1007 | `session.ts:253` returns the secret key; no reachable-location operation exists | C6.4 | handoff URL resolves for a recipient outside the sandbox; no secret in the returned value |
| R-1008 | `HeadObject` reaches `manifest.save()` (`objects.go:138`) | C6.3 | concurrent adoption/listing/mutation from Go, WASM, and S3 under `-race`, no deadlock |
| R-1009 | `live.yml` has `require_configured`; `release.yml:172` gate skips by default | C6.5 | release fails when no provider is configured |
| R-1100 | — standing constraint | — | §3.3 divergences carry conformance cases; §7.4 chose parity over a stronger claim |
| R-1101 | `grep "BackendWasm\|\"wasm\"" internal/runtime` → no backend | — standing constraint | no document describes WASM as a store |
| R-1102 | — standing constraint | — standing constraint | — |
| R-001 | `stow-environment.md` §0.2, §21.2 stale | M0.4 | §0.2.1 row closed |

---

## 6. Sequencing

```
M0.1 gate tests ──┬─► M1.8 four shipped bugs ─┐
M0.2 suppression ─┤   (C1.1 C1.2 C1.4 C1.5)  │
M0.3 gate scope ──┤                          │
                  └─► Track C ───────────────┤
                                             │
M1.1 capabilities ─┬─► M1.4 Environment ─┬─► M3.2 descriptor  │
   (🟡 half-done)  │                     │                    │
                   └─► M1.6 profiles ────┴─► M3.3 backend    │
                                                               │
M1.2 Store field ─┬─► M3.1 multi-env                          │
                  │                                             │
M1.3 authority 🟡 ┼─► M2.4 upstream wall ──► M2.7 write-path   │
  4 of 12 dead    │   (C1.3 is the fix)       integrity        │
                  │                             │              │
M1.5 identity ────┴─► M4.3 promote ⭐                         │
                                                               │
M2.0 sandbox ────► M2.6 "sandbox"                             │
   ▲                          │                                │
   ├─ M2.1 namespace ─┐      │                                │
   ├─ M2.2 filesystem ─┤      │                                │
   ├─ M2.3 quotas ─────┼─► M3.1 ──► M3.0 ──► M3.5              │
   └─ M2.5 lifetime ───┘   (gated on authority)  (C4.6)        │
                                                    M3.4 ─────┘

M2.8 session lifecycle ─────────────────────────────────────────┐
   ▲                                                            │
   ├─ C6.1 read-only opens ─────────┐                           │
   ├─ C6.2 collection triggers ─────┼─► session primitive       │
   ├─ C6.3 shared-dir concurrency ──┤   mint→work→handoff→collect│
   ├─ C6.5 release fails on skip ───┘   (independent, 1 flag)    │
   │                                                                 │
   └─ C6.4 handoff URL ──► gated on M2.7 ──────────────────────────┘
      (convergence presupposes propagation that works)

W14 distribution — parallel, blocked on nobody, longest lead time
```

**Two critical paths of different lengths.** Trust: M0.1 → everything; M1.3
cannot be re-closed without M0.4, and no Done when is mechanical without M0.1.
Environment: M1.1 → M1.3 → M2.0/M2.1–M2.3 → M3.1 → M3.0 → M3.5.

**Not on either path:** M4.3 (promote) is independent and, per M4, may be much
smaller than the roadmap assumes. W14 is independent of everything. Track C is
independent except `C1.1 → M2.3` and `C1.3 → M2.4`, where correctness work feeds
a milestone's audit.

### 6.1 First three days

Re-ordered against verified status: M1.1 and M1.7 are partly or wholly done, so
the ordering that preceded them no longer applied.

1. **C5.3** — the `len(parts) == 0` guard into `ValidateMultipartPartNumbers`, the
   `len(parts) > 1` conditional deleted, four contract tables. Two production
   lines; it is what stops C1.1 recurring.
2. **C1.1, C1.2, C1.4, C1.5** — four shipped correctness bugs, each with a test
   that is red on `1982104`. Commit separately, red then green.
3. **C6.1** — a read-only authority can open a workspace. One line of intent
   (`workspace.go:133` must not evaluate the grant it is issuing) plus a test.
   A documented capability that cannot be invoked is worse than an absent one.
4. **C6.5** — release fails on a skipped live gate. `live.yml`'s
   `require_configured` already exists and is unused; this is a one-flag change
   that stops the release path asserting a verification it did not perform.
5. **M0.1** — nothing else in this document is verifiable until it lands.
6. **M0.4** — reopen M1.3 and add the anti-recurrence assertion.
7. **C2.1** — `internal/atomicfile`; small enough to finish while C1.3 is in review.
8. **ADR 0010 decision 2 implemented** — the authority carried into
   `runthrough.NewWithOutbox` — then C1.3 design. The highest-value structural
   item, and the one that was blocked on a wiring constraint rather than on code.

---

## 7. Decisions

All four questions this document originally left open were decided on 2026-09-26.
Three are recorded in
[ADR 0010](../adr/0010-an-enforcement-site-or-not-a-permission.md); the fourth
is a compatibility claim and lives in the document that owns the S3 surface.

| # | Decision | Requirement | Recorded in |
|---|---|---|---|
| 7.1 | Enforce `EnvironmentDestroy`, `UpstreamRead`, `UpstreamWrite`. **Remove** `EnvironmentPromote` until M4.3 | R-101, R-104 | ADR 0010 decision 1 |
| 7.2 | **Delete** the `pkg/stow.Store` seam; treat removal as a breaking change | R-910 | ADR 0010 decisions 4, 5 |
| 7.3 | Keep Track C inside this specification rather than splitting it out | — | — |
| — | Land the gate tests before reopening M1.3 | R-1001…R-1004 | ADR 0010 decision 6 |
| 7.4 | Merged `ListObjectsV2` claims **eventual** consistency, not a snapshot | R-707 | [`docs/compat-contract.md`](../compat-contract.md) §6.2 |

**No open decisions remain.**

### 7.4 Why eventual consistency, and why that was nearly missed

The question was whether a merged local/upstream listing must claim a snapshot.
It cannot, and the reason is structural rather than a matter of effort: the local
store and the upstream provider are independent systems, so the result is a union
of two separately-timed reads. Any implementation that returned a true snapshot
would have to stop merging and pick one source, which defeats the feature. The
real choice was *which kind of non-snapshot* to be, and to say so.

**Eventual** is also the AWS-compatible answer. The stated compatibility target
is the observable behaviour of pinned AWS SDK v3 and AWS SDK for Go v2
(`docs/remediation-plan.md`), and S3's own `ListObjectsV2` has never promised a
snapshot. Claiming stronger consistency than the compatibility target would be a
divergence presented as an improvement.

The contract also had to be untangled from the implementation. It previously read
"*Implementation fetches complete prefix sets from both sources, merges, then
applies client `max-keys` / continuation locally so pagination tokens stay
stable*" — a description of the O(n)-per-page strategy that R-707 replaces. Left
alone, landing R-707 would have made the acceptance document silently false. It
now states the ordering, the opacity of continuation tokens, and the consistency
guarantee, and names the requirement that bounds the fetch.

**Consequence for testing:** no conformance case may assert an ordering or
completeness property across pages, because none is guaranteed. A case may assert
that keys from both sources appear, that a duplicate key resolves to local
metadata, and that a bounded fetch does not re-read the full set.

---

## 8. Failure injection and property tests

Required by the M2 walls; not yet present. `grep -rn "func Fuzz"` returns 0.

| Target | Property |
|---|---|
| `internal/storage/fs` key encoding | `objectKeyFromSegments(objectRelSegments(k)) == k` for all inputs, including every chunk boundary. The existing `longkeys_test.go:376` covers 100, 127, 128, 200, 201, 250, 256, 300, 400, 401, 512, 1024 |
| `parseCopySource` | decoding is total; a source that decodes to an empty bucket or key is rejected, not panicked |
| `parseRange` | no input produces a range outside `[0, size)`; no integer overflow on a maximal `int64` end |
| `ValidateMultipartPartNumbers` | total: rejects the empty set, unsorted sets, and duplicates |
| `atomicfile.Write` | the destination is either the old content or the new content after any interruption; never a partial file |
| `outboxState` transitions | the sum type admits no illegal transition; no path reaches a terminal state twice |
| `Authority` attenuation | `child.IsSupersetOf(parent) == false` for every child derived by `Without` |

---

## 9. Assumptions register

Unverified claims this document depends on. Each is an assumption, not a finding.

| # | Assumption | If wrong |
|---|---|---|
| A-1 | **CORRECTED 2026-09-26.** Previously recorded here as "the live suite never runs in CI" — **that was wrong.** `.github/workflows/live.yml` exists and `release.yml:172` calls it as a `live_gate` job that `release.yml:195` puts in the release's `needs`. It runs `./conformance/live-provider.sh test` with `STOW_CONFORMANCE_UPSTREAM=1`, `STOW_CONFORMANCE_DISPOSABLE=1`, a two-profile matrix, and a dry-run mode. The real position: the gate **skips by default** and a skip still permits a release, recorded as `SKIPPED - no live provider was configured, so this release is NOT verified against one` with a workflow warning. The machinery to enforce it already exists — `live.yml`'s `require_configured` input fails instead of skipping — and is not used | §3.8 R-1009 is unimplementable-as-written and the real-S3 divergences in §3.3 stay unconfirmed. Enforcing is a one-flag change, so the assumption is now about *policy*, not capability |
| A-2 | The 16 functions in `conformance/local_test.go` map onto the 15 corpus cases | C5.4's edit either loses coverage or duplicates it; the file-size entry cannot be retired cleanly |
| A-3 | R-704's wedge is reachable in practice | The `outboxOwnerAlive` removal in C4.3 is a simplification rather than a fix. **Partially retired 2026-09-26:** `Renew` *is* called at `outbox_claim_adapter.go:64`, so a live owner keeps its own lease and the lease alone is a sufficient liveness mechanism — the liveness check is redundant, not load-bearing |
| A-4 | **RESOLVED** by ADR 0010 decision 4 — `pkg/stow` is consumed only by the wasm bridge, so the seam is deleted | The assumption no longer carries risk. If a caller outside this repository implements `stow.Store`, ADR 0010 decision 5 makes the break explicit and versioned |
| A-5 | No gate in `release.yml` is skippable | A release can ship with a failing quality job, contradicting CODE_STANDARDS.md:91 |
| A-6 | `cmd/stow-s3/main.go:75-78` is the only place capabilities are computed | M1.1's single-source requirement has more than one producer, and M3.2's descriptor inherits the duplication |
| A-7 | The four baselined `max-params` entries in `internal/s3api` are the ones C3.4 removes | The baseline does not reach zero, and §11's claim about ratchet descent is wrong |

---

## 10. Engineering decision rule

Binding on every change in this document.

1. **A line number is not a claim.** Read the line, run the code, or mark it
   unverified. This rule has already been broken twice in this repository's own
   documents (§0.2.1).
2. **A commit subject is not a status.** M1.3 was closed on incomplete criteria by
   a commit titled "*authority, enforced below every interface*". Status comes
   from re-tracing the item's own Done when.
3. **A requirement without a verifying test is a wish.** Every `R-*` in §3 appears
   in §5 with a test. Adding a requirement means adding its row.
4. **Prefer deleting a copy to adding a check.** §4.1 lists six decisions written
   down more than once. A fix that adds a check without removing a copy has not
   fixed the defect.
5. **A baseline is a ratchet, not a ledger.** CODE_STANDARDS.md:45-47 makes both
   a new identity and a **stale** identity a failure. Retire the six
   `max-params` entries (C3.4), the three `any` entries (C3.3), `serve`'s
   complexity entry (C4.1), `parseRoute`'s entry (C3.4), and the one file-size
   entry (C5.4), and `go-quality.json` reaches 0 with `file-size.json` — provided
   each is lowered in the same commit as its fix. The gate refuses; no one has to
   remember.
6. **Judge coverage per package.** `go-coverage.json` is an aggregate floor
   (64.4%). The shipped entry points are `cmd/stow-s3` 41.7%, `internal/s3api`
   58.7%, `pkg/stow` 55.1%.
7. **Do not go looking for a wrapper chain in `internal/runthrough`.** See §1.1.
8. **Specify the product surface, not only the defects.** A specification
   assembled from a review is weighted toward correctness of what exists, because
   that is what a review produces. Four of the end state's requirements — a
   constructor that must succeed (R-1005), three collection triggers (R-1006), a
   concurrency property (R-1008), and a handoff operation (R-1007) — are not
   defects at all, and a defect-only process will not find them. When a target
   names a capability, it gets a requirement and a test even though nothing is
   currently broken. §12 is the check that this happened.
9. **Re-verify your own claims before shipping the document.** A-1 in §9 was
   wrong in this document: it asserted from a grep of one test file's skip
   condition that the live suite never runs, without reading
   `.github/workflows/live.yml`. That is the same error §0.2.1 records twice
   about `stow-environment.md`. Every claim in §0.2 and §2 carries a re-check
   command for that reason — use it rather than trusting the table.

---

## 11. Destination

Stow knows what it is allocating, and enforcement lives below the interfaces.
`stow()` costs what `mkdtemp()` costs. Everything above the store is composable;
everything below it is conformant.

The measure of success is not this document. It is that `make standards` fails
when a permission is decorative, when a gate is bypassed, and when a backend
diverges — and that no milestone can be closed on prose again.

---

## 12. Coverage of the end state

The end state is seven build items plus a deployment topology. This table maps
each to what this document already requires, so the gap is explicit rather than
implied. Verified against `1982104`.

| End-state item | Covered by | Status |
|---|---|---|
| **1. Faithful local S3** — SigV4 as AWS SDKs sign; 304/206-clamp/412; multipart ETags and abort cleanup; checksums computed and compared on every write | R-305, R-306, R-307, R-303, R-304, R-302 | **Mostly covered.** SigV4, 304-vs-412, abort cleanup, and checksum verification needed no requirement — all four verified correct. Two shipped divergences open: `C1.2`, `C1.4` |
| **2. The session primitive** — mint → work → hand off → collect; three collection triggers; resume-by-ID; Destroy ownership; handoff never returns the secret | R-1005, R-1006, R-1007 | **Was a gap. Now specified.** 1 of 3 collection triggers wired; read-only workspaces cannot open; handoff-as-capability-URL did not exist as a named operation. Resume-by-ID and Destroy's protected-path backstop are covered by ADR 0009 and `workspace/protected_test.go` |
| **3. The shared working directory** — host-written files adopted safely; no deadlocks, no data races, correct under concurrent Go/WASM/server access | R-1008, R-802 | **Was a gap. Now specified.** The manifest lock is correct and the fast path is fast, but there was no stated property and therefore no test. A read reaches the write path today, so the hazard is reachable |
| **4. Authority below every interface**, including Destroy and upstream; read-only workspaces open and cannot mutate | R-101, R-103, R-104, R-201, R-1005 | **Enforcement covered; the read-only case was not.** Enforcement is 8 of 12 operations. Read-only workspaces fail at the constructor, so the second half of the item was unreachable |
| **5. Run-through that runs through** — fall-through reads; propagation with claim leasing and fencing; reconcile-before-propagate; no silent divergence; routine crash recovery | R-201, R-701–R-708 | **Covered and then some.** Eight requirements; the outbox state machine, the fencing gate, the PID wedge, the swallowed errors, and the unbounded fetch were all unaddressed. Reconcile-before-propagate and crash recovery already exist and are correct |
| **6. Crash honesty** — temp → fsync → rename with directory syncs whose errors are **surfaced, not swallowed** | R-801, R-802 | **Covered.** `fs_atomic.go:40-43` swallows both the open and the sync error; `manifest.go:123` omits the directory sync entirely. R-801 requires the errors be returned |
| **7. Shipped surfaces** — npm and PyPI published; WASM honest about being in-process; the release gate always verifies against a real provider | R-1009, R-1101; W14 (§10) | **Partly covered, and A-1 was wrong.** The live gate exists and is wired into the release's `needs` — but skips by default and a skip still releases. npm and PyPI are unpublished (W14, the longest-lead track). WASM's honesty is already recorded in `stow-environment.md` §0.2.1 and R-1101 keeps it |
| **Handoff = capability + location** | R-1007 | **Was an unexamined assumption.** My spec inherited "promotion is a local operation" from ADR 0009 §4 and never asked whether handoff has a *reachability* obligation. It does, and it is a different operation from promotion. Gated on M2.7 |

### 12.1 What this mapping changed

Three of the seven items were less covered than this document's tone implied.
The specification was weighted toward *correctness of what exists* — which is
what a review produces — and the end state names four capabilities that are not
defects at all: a constructor that must succeed, three collection triggers, a
concurrency property, and a handoff operation. A spec built only from found
defects will systematically under-specify product surface, because product
surface has nothing to review.

The fourth correction is mine. A-1 claimed the live-provider suite never runs in
CI. It does — `.github/workflows/live.yml`, called by `release.yml:172`. I wrote
that claim from a grep of one test file's skip condition without reading the
workflow, which is the same mistake `stow-environment.md` §0.2.1 records twice
about itself, and the reason §0.2.1's rule exists. A-1 is corrected in §9 with
the accurate position: the gate exists, skips by default, and the flag to stop
that is already implemented and unused.
