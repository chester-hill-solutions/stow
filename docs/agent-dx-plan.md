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
3. **The admin surface had no credential.** `--allow-public-admin` was the only
   gate, so enabling it exposed the destructive `outbox/retry` and
   `outbox/discard` actions to anyone who could reach the port. The flag is now
   deprecated and ignored; remote admin requires an admin token, and the
   destructive routes require it on loopback too, because loopback is not a
   privilege boundary. Read-only routes stay loopback-open so `stow doctor` is
   unaffected.
4. **CORS reflected any origin.** Any website a developer visited could read
   responses from their local stow, with the credentials the SDK had already put
   in the page. Now an allowlist defaulting to loopback, `Vary: Origin` on every
   response, and a refused preflight. This also wires `Config.CORSOrigins`,
   which had been declared and never read.

Still open, in descending order of harm:

5. **The macOS parent-death watch is a no-op.** `internal/parentwatch` opens a
   kqueue descriptor and `defer`-closes it the instant `Watch` returns; no
   goroutine ever services the registered `NOTE_EXIT` event. macOS arm64 is a
   first-class release platform by the decision in section 17, so sessions can
   orphan there. Windows is `ErrUnsupported`, and `parentwatch_test.go` has no
   build tag, so it does not even compile on that platform. There is no macOS or
   Windows CI job. The fix is small but cannot be verified on this host, so it
   wants a macOS runner rather than a blind edit.
6. **The filesystem key limit is ~127 bytes, not the documented 1024.**
   `storage.ValidateKey` accepts 1024, but keys become
   `hex.EncodeToString` filenames and `NAME_MAX` is 255, so a 128-byte key fails
   `ENAMETOOLONG` on every supported filesystem. The Phase 2 exit criterion names
   128-byte keys, and no test anywhere covers a long key.
7. **The TypeScript client ignored two of its own options.** `timeoutMs` and
   `signal` were declared on `EphemeralStowOptions` and never forwarded, so a
   caller's timeout was replaced by a hardcoded 10 s and an abort did nothing.
   The ready-descriptor parser treated a partial record as complete, and a parse
   failure inside a stream handler rejected nothing, so a truncated record hung
   until the timeout. The server's reported region was parsed and then discarded
   in favour of a hardcoded `us-east-1` — and because the server had no way to
   report any other region, the field could not even be observed to be wrong.
   Cancellation was reported with the error code `internal`, which tells a caller
   nothing about whether retrying could ever help.

   Closed. Both options are forwarded and honored, an already-aborted signal
   fails before a process is spawned, a mid-startup abort stops the child, only
   a newline-terminated record is parsed and a parse error rejects rather than
   escaping as an uncaught exception, the descriptor's `end` and `error` events
   are handled, and `cancelled` is a real error code. The server gained
   `--region`, because honoring a reported region is untestable while the
   reported region cannot vary.

8. **The `check-generated` gate could not pass off its build machine.**
   `build-wasm` omitted `-trimpath` while the native build had always used it, so
   the committed WASM artifact embedded its own source path and never matched a
   build from a fresh clone. The gate compared a committed file against the same
   source built at a different path and reported a difference that was not one.

   Closed in 6480755, after confirming that two directories produced two hashes
   for the same commit and that `-trimpath` made them byte-identical.

Item 6 is the one that invalidates a stated exit criterion rather than merely
adding work, and it is also the prerequisite for the storage format v3 in the
10-plan: that change is a hash-filename scheme, which is the same fix.

The recurring lesson across items 1 to 4 is worth recording. Each was a
*permissive default* that the surrounding prose described as safe, and each sat
in a phase that a document had already marked complete. Two were reachable from
a published artifact. Neither a status table nor a passing gate would have found
them; reading the code did.

### 0.5 Revision 2: the default is a workspace, not a scoped S3 session

This revision records a change of product direction, not a change of schedule.
It is dated 2026-09-25 and it amends sections 1, 3, 4, 10, and 16 of this
document. Three ADRs carry the decisions:

- **ADR 0007** — the workspace is the default; S3 is an opt-in facade.
- **ADR 0008** — a workspace stores objects as real files, with metadata in
  one manifest.
- **ADR 0009** — a workspace outlives the process that created it.

The buildable specification is `docs/workspace-contract.md`: the concrete
interface in three languages, the key-to-path encoding, the manifest format, the
error codes, and the fourteen conformance cases. The ADRs decide; that document
specifies.

#### What changed and why

The plan built the right thing for the wrong default. Section 4.1 made a
short-lived child S3 server the default agent surface, and sections 1 through
9 then optimised it: a versioned ready protocol, SigV4 off stdout, generated
credentials, memory quotas, a tighter collector, a platform binary. All of
that work is sound and none of it is wasted — it is the S3 session profile,
and it stays.

What it optimised was a surface that is the wrong default for the product
being described. Three measured and code-level facts forced the change:

1. **The cost is wrong for something a framework starts per task.** 11.6 MB of
   fixed process RSS, 15.4 ms p50 to ready, 35.5 ms p50 shutdown, measured on
   Linux x64. The per-session byte cost has already been fought from 4.59 MB
   per MiB to 3.27 MB; the fixed cost is untouched by collector tuning and
   will not yield to it.
2. **The data directory is not a directory.** Objects are base64 inside
   per-object JSON records (`internal/storage/fs/fs_records.go:14`). An agent
   cannot read a file it was told to read. A developer who wants real files
   creates a temp directory first and starts Stow beside it, which makes
   Stow a sidecar to the thing it should be.
3. **The public in-process runtime cannot hold a workspace at all.**
   `pkg/stow/types.go:10` exposes one backend, and `runtime.Open` rejects any
   other without an injected store (`internal/runtime/instance.go:64`).

The third fact is the one that matters most for planning. The in-process
default is not a refactor of something that exists; it is blocked behind a
persistent backend in the public runtime that does not exist yet.

#### What this invalidates

| Section | Status |
|---|---|
| 1, "Product outcome" | Amended. The outcome is a bounded artifact workspace; the S3 endpoint is one capability of it |
| 3, principle 3, "Ephemeral by default" | **Superseded.** Durability is bounded by a TTL, not by scope exit. See ADR 0009 |
| 4.1, "Default implementation: managed child S3 endpoint" | **Superseded for the default path.** It remains the documented shape of the S3 session profile. See ADR 0007 |
| 4.2, direct embedded runtime as a separate profile | **Reversed.** It becomes the default where a client can embed it. See ADR 0007 |
| 4.3, no shared daemon | Unchanged. ADR 0009 keeps it |
| 10, prioritized backlog | Reordered below |
| 16, first execution slice | Superseded below |

#### Status of the eight product requirements

| # | Requirement | State in this tree | Evidence |
|---|---|---|---|
| 1 | Zero-friction install | Partial; blocked on accounts, not code | Go module live. `published: false` at `scripts/check-install-surface.mjs:32,41`. Windows parent-death watch is `ErrUnsupported` (`internal/parentwatch/parentwatch_unsupported.go`) and `parentwatch_test.go` has no build tag, so it does not compile there |
| 2 | Default workspace, not a sidecar | **Absent** | ADR 0007, ADR 0008. Objects are JSON records, not files |
| 3 | Survive the process | **Inverted today** | Memory backend and `mkdtemp` by default (`session.ts:109,117`), removed on close (`:269`), `parentPid: process.pid` (`:124`). No registry, no resume, no promote. ADR 0009 |
| 4 | Framework embed | Absent | Two first-party clients, no integrations. `stow mcp` exists only as prose in `docs/agentic-dx-10-plan.md:186` |
| 5 | Human distribution | Partial, and one part is blocked | Presign works. No relay, no preview (ADR 0009 §6 keeps a relay out of scope). Promote is blocked on run-through mode having never read or written upstream — see 0.6 defect 1 |
| 6 | Safe by construction | Mostly done, one cheap finish | Virtual-hosted parses (`s3api/router.go:25`) but `conformance/CONFORMANCE.md:98` records the smoke test as pending, and the client hardcodes `forcePathStyle: true` (`index.ts:104,109`) |
| 7 | Quotas and policy | Half | Bytes, objects, body cap, CORS allowlist, admin token done. Missing: request rate, wall clock, audit log. Stow can refuse to make outbound calls; it cannot stop the *agent* from reaching the network |
| 8 | Cheaper than a temp directory | Half, and unreachable for the current shape | Measured figures above. ADR 0007 section 3 restricts the cost target to the embedded path |

#### Two constraints on the new plan

- **The in-process default does not hold for Python.** `packages/stow-s3-py`
  spawns a server and there is no embedded path. The *workspace* is uniform
  across languages; the process is not. Any cost figure must be attributed to
  a path, never averaged across both. See ADR 0007 section 3.
- **`close()` stops meaning "the data is gone."** That is a breaking change to
  both client packages and it invalidates a passing lifecycle test. See ADR
  0009 sections 3 and Consequences.

#### What is not being re-decided

More S3 operations, a dashboard, a vector store, a memory graph, a tool
marketplace, WASM for its own sake. Each would help a specialist and none
would make this the default. The product bar is that an agent runtime starts a
bounded artifact workspace the way it already starts a sandbox, and that some
of those workspaces speak S3 so existing and generated code keeps working.

### 0.6 Inherited defects this plan is standing on

This section exists because the plan above is being written on top of a tree
that has now produced the same failure three separate times: **the feature is
present, the tests are green, and neither can see the defect.** Two permissive
defaults shipped from a phase a document had already marked complete (ADR
0005, ADR 0006). A 3,646-line subsystem — 30% of production Go, 75 passing
tests — has never once read through to an upstream.

The lesson is not "write more tests." It is that the *shape* of a test decides
whether it can fail, and that a status table records intent rather than
behaviour. So the known-broken items are listed here, with evidence, *before*
the backlog is sequenced. Anything in section 10.1 sitting on one of these is
either blocked or must carry its fix.

| # | Defect | Evidence | What it blocks |
|---|---|---|---|
| 1 | **Run-through never reads or writes upstream.** `resolveObject` treats `ErrBucketNotFound` from *either* store as a final answer, so the cache-miss path is unreachable and the only cache writer is never called. Writes 404 first: `Instance.PutObject` runs a `HeadObject` quota pre-check before the outbox is touched. All four bucket operations are local-only, so upstream buckets are invisible | `internal/runthrough/adapter.go:305,326,376`; `internal/runtime/instance.go:192,425`; `cmd/stow-s3/store.go:28` | **Promote.** Requirement 5's "promote to the user's real bucket", and W8, are built on a subsystem that has never moved a byte. Promote is either blocked on this or scoped to a local copy with an explicit refusal |
| 2 | **The cache adapter special-cases one of two missing sentinels.** Both stores return `ErrBucketNotFound`, never `ErrObjectNotFound`, for a missing bucket. The correct helper already exists and is unused at the fault line | `internal/storage/fs/fs.go:126`; `internal/storage/memory.go:204`; `internal/runthrough/cache_policy.go:139` | The workspace backend is a *third* `storage.Store`. Anything that layers stores inherits this trap |
| 3 | **The request-body cap cannot be raised.** `server.go:220` applies `MaxBytesReader` at `s.config.MaxRequestBytes`, and `cmd/stow-s3/main.go:77` sets it to the constant — with no flag and no option. A `PUT` above 8 MiB fails with `EntityTooLarge` whatever the caller asks for | `internal/s3api/errors.go:63`; `cmd/stow-s3/main.go:77` | Requirement 7, and any workspace where an agent legitimately writes a large file through S3. A quota a host cannot raise is not a quota, it is a surprise |
| 4 | **Two unimplemented S3 features return the wrong error code.** 18 sub-resources correctly return `NotImplemented`; `versions` and `location` are omitted from the list and fall through to `InvalidRequest` | `internal/s3api/dispatch.go:121-138` | Requirement 6, which is explicitly about *error codes SDKs already understand*. An SDK feature-gate keyed on the code takes the wrong branch. Two entries to add |
| 5 | **The coverage floor is an aggregate, and it hides the shipped entry points.** 62.38% of 5,253 statements is the floor; `cmd/stow-s3` is 40.9% and `internal/s3api` 55.9% | `scripts/baselines/go-coverage.json` | W1 adds a new public entry point. An aggregate floor lets the new path ship uncovered while the total still rises |
| 6 | **An ambient AWS profile silently switches a server into run-through mode.** `DetectMode` selects run-through whenever the upstream variables resolve, which the `AWS_*` fallback makes easy. `STOW_MODE=local` is the escape hatch and is not discoverable | `internal/runthrough/config.go:213` | Requirement 6's safety claim, and the workspace default. A workspace must never auto-detect an upstream |
| 7 | **The user-facing docs still describe the superseded default.** `site/agent.md`, `site/llms.txt`, and `skills/stow-s3/SKILL.md` all present `withStow`/`with_session` as *the* pattern, and the skill's own description says "a bucket that is thrown away afterwards" — the exact default revision 2 removes. `check-install-surface.mjs` reads those files for publication status only | `site/agent.md:66,86`; `skills/stow-s3/SKILL.md:3,69,90` | Adoption. The install surface is the first thing an agent reads, and it currently teaches the old contract. No gate catches this, which was predicted when those files were created |
| 8 | **`npm view` reports a package a consumer cannot install.** On this machine the `@chester-hill-solutions` scope is bound to `https://npm.pkg.github.com` in `.npmrc`, and a scope binding beats `--registry`. So `npm view @chester-hill-solutions/stow-s3 version` answers `0.2.0` for a package that is not on npmjs at all | Verified 2026-09-25 by direct HTTP: npmjs `404`, GitHub Packages `401`, PyPI `404` | Any "is it published?" check done with `npm view` on this machine is wrong, including a human's. Query the registry API with `curl` and read the status code. This one produced a false positive during this very work |
| 9 | **The install-surface gate's disclaimer rule is inverted.** A *published* target must be accompanied by a disclaimer matching `/not published\|not yet\|not on npm\|not on PyPI\|404/`, so a document that correctly says "the Go module is published and works" is failed for not also saying that something else is unpublished. It passes today only because the prose happens to contain those words elsewhere | `scripts/check-install-surface.mjs:84` | W13. That work extends this pattern to declare the *default* once in a script, so extending a rule with a known backwards test propagates the bug. Fix the rule first, or do not build on the pattern |
| 10 | **`npm ci` cannot run, so the TypeScript verification path is dead on main.** The lockfile carries four entries for the platform carrier packages with **no `version` and no `resolved`**, so npm refuses with a lockfile-desync `EUSAGE`. The `npm error aliases: …` line is npm listing its own command aliases, and it is a red herring that sends you looking at the wrong file | `packages/stow-s3/package-lock.json` | `make test-node`, and therefore `make check-generated`, and therefore the last step of `make standards`. Found while trying to verify W3 in TypeScript; committed breakage, not local drift |
| 11 | **The documented `STOW_BIN` precedence is the opposite of the implemented one.** The comment claims an explicit `STOW_BIN` wins for development; both clients check the bundled platform package first, and three tests encode the comment rather than the code | `packages/stow-s3/src/bin.ts`, `packages/stow-s3-py/src/stow_s3/bin.py` | The code is right — a pinned install should outrank a stale exported environment variable — so the comment was the defect. The failure is invisible until a platform package is present in the tree, which is precisely the state the dev install must avoid |

#### Harness rules for the workspace backend

Defects 1 and 5 above share one root cause, and W0 is a new store that will hit
it immediately. In `internal/runthrough`, all 13 adapter test constructions pass
the *same* store as both local and cache, while production passes two distinct
stores. With `cache == local`, a cache lookup can never fail independently of
the local one, so the broken branch was unreachable. The six tests that did use
distinct stores all pre-seeded `cache.CreateBucket`, which made the cache agree
with the local store about which buckets exist.

Three rules follow, and they bind on W0:

1. **When a constructor takes two dependencies of the same interface, compare
   what the tests pass against what `main` passes.** If they differ, an entire
   configuration is untested. The workspace backend plus its manifest is
   exactly that shape.
2. **A fixture that pre-creates the parent of the thing under test makes the
   test unable to fail.** The harness's *default* must be the broken case, and
   a test must opt in to seeding. Not a comment asking people to remember.
3. **The same-bytes test belongs in the corpus, not in a hand-written
   expectation.** A test that a mock can satisfy has not tested the claim in
   ADR 0007 section 5.

#### One technique worth keeping

Run-through can be exercised end to end with no cloud account by starting a
second `stow` as the upstream and reading its address and credentials off the
`STOW_READY` line. That is how the read-path defect was reproduced rather than
merely reasoned about, and it is how W8's promote path gets tested without an
AWS account. Note that the access log prints every request as `GET /s3`
regardless of bucket and key, so the log cannot tell you which object was
touched; use `/_stow/inspect` instead.

### 0.7 Market position and the wedge

Added 2026-09-25, after `docs/competitive-landscape.md` Part 2. This section
narrows the thesis. The framing in section 0.5 described a direction that turned
out to be a shipping product category, so the plan is re-derived from what is
actually open.

#### What the market did while this plan was being written

| Requirement | Who has it | Grade |
|---|---|---|
| 2. Workspace that is the cwd, same bytes as S3 | Cloudflare Sandbox mounts buckets as local paths; a major cloud vendor has shipped the convergence itself | A / C |
| 3. TTL-bounded durable scratch | Ordinary. A documented three-day default TTL, extendable | A |
| 4. Framework embed | The Agents SDK shipped a first-party sandbox abstraction with named storage mounts and seven official providers; a Kubernetes SIG standard exists with Python and Go clients | A |
| 6. S3 wire compatibility | Every emulator and every cloud. Never was a differentiator | A |

**So the concept stopped being the product.** Being right about the category was
worth something in early 2026 and is worth very little now. Section 0.5's
"honest bar" — that every agent runtime starts a bounded artifact workspace — was
already true when it was written, and it is the wrong bar to aim at.

#### The two exits, which are the actual opening

The two most widely used local S3 options both stopped being zero-account within
about five weeks:

- **MinIO community edition**: the repository states it is no longer maintained
  and that community distribution is source-only. Unmaintained, plus source-only,
  plus a named commercial successor — three separable facts, and this plan does
  not assert an archive date, because no first-party one was found.
- **LocalStack**: from 2026-03-23 a single unified image requires an auth token
  *including in CI*, with a temporary bypass that expired 2026-04-06. Active and
  well funded, not discontinued — what ended is starting with no account at all.

That is a stronger position than a merely validated category. A validated
category with occupied seats is hard. This one had its lowest-friction occupant
leave.

#### The wedge, stated as a distribution claim

Every competitor in the landscape requires at least one of: a cloud account, a
cluster, a deployed edge function on a paid plan, a container runtime, or a
provider sign-up. The two exits removed the two ways that used to be satisfiable
locally and for free.

What remains is narrow:

> **The only S3-shaped workspace an agent or a CI job can start with no account,
> no container runtime, and no path to production credentials — and which is also
> that process's working directory.**

This is won or lost on whether installation works. **The account-side unblock in
section 10.1 is therefore the precondition for the strategy, not a packaging
chore.** A correct, well-tested workspace that cannot be `pip install`ed is not
more useful than the alternatives; it is less useful, because it also asks for a
build.

#### What this changes in the plan

1. **W7 is rewritten.** It is no longer "define the embed surface." It is
   conformance to surfaces other people own: the Agents SDK sandbox manifest,
   whose storage concept is a *mount* rather than a *workspace*, and the
   Kubernetes SIG agent-sandbox API. A smaller prize, better specified.
2. **The concept claims are demoted.** Sections 0.5, 1, and 16 keep the
   direction, because a workspace that is the working directory is still the
   right shape. They stop being the *argument*.
3. **Requirement 1 is promoted** from one P0 among many to the gate on the whole
   strategy, and given its own phase with its own exit condition.
4. **A competitive response is added.** The LocalStack displacement is a
   concrete, dated, addressable event, and Stow's zero-account property is the
   direct replacement for the thing CI pipelines lost on 2026-03-23.

#### What did not change

The technical work. W0 and W1 built the workspace backend and put it behind a
public API, and that is the right foundation for this wedge rather than for the
old one — the wedge is "starts with nothing," and an in-process runtime with no
listener, no port, no credentials, and no process is exactly what starts with
nothing.

### 0.8 W3 correction: the breaking change does not apply to a session

Recorded 2026-09-25, after implementing `Destroy` in Go.

This plan said W3 would make `close()` non-destructive in the TypeScript and
Python clients and replace the "leaks no processes or directories across 100
sequential sessions" assertion in the same diff. **That was wrong, and following
it would have made things worse.**

The session profile is **memory-backed** (`packages/stow-s3/src/session.ts:117`).
Its objects live in the child server's heap, and the child is reaped on close —
so the bytes are already gone before the data directory is. Deleting that
directory on close destroys nothing of value, which is exactly why ADR 0004's
dispose-on-scope-exit is still correct for that profile.

Making the session's `close()` non-destructive would therefore:

- preserve nothing, because there is nothing durable to preserve;
- break a documented contract for no benefit;
- and **make the property that test protects strictly worse** — 100 sessions
  would leave 100 directories on disk instead of none.

So the split is profile-specific, and the plan conflated the two:

| Profile | Backend | `close()` | `destroy()` |
|---|---|---|---|
| S3 session (TypeScript, Python) | memory, child process | disposes, as ADR 0004 says | unnecessary; the bytes are already gone |
| Workspace (Go, and the clients in W7) | persistent files | releases the handle, deletes nothing | removes the directory |

`resume` is likewise W4, and belongs to the workspace, not the session.

What the clients still need from W3 is only the *vocabulary* — a `destroy` that
is explicit rather than implied — so that when the workspace surface lands in
TypeScript and Python under W7, the semantics are already settled and identical
across the three languages. That is a naming and API-shape task, not a
behavioural break.

### 0.9 Adoption is why `Destroy` needs an ownership record

`Destroy` turned out to need a decision the ADRs had not made, and it is worth
stating because it is the kind of thing that is obvious in hindsight and
catastrophic if missed.

A workspace is *designed* to be pointed at a directory the caller already has —
`openWorkspace({ dir: process.cwd() })` is the documented way to use one. So
"this directory contains a stow manifest" is not evidence that its contents
belong to stow. Had `Destroy` keyed on the manifest, the documented way to use
this package would have been a way to delete someone's project.

The manifest therefore records whether stow **created** the directory or
**adopted** it, decided before anything is created and preserved across reopens.
Two defences then have to agree, and they are deliberately redundant:

1. **Ownership** is primary. An adopted workspace can be read, written and
   closed; removing it is the caller's business.
2. **The protected-path check** is the backstop, ported from ADR 0006. It is the
   layer that still holds when a workspace legitimately lives somewhere that has
   since become someone's home or working directory, or when a caller guesses a
   path another stow process once used.

Two bugs were caught by writing the tests before trusting the logic, both worth
recording:

- The ownership decision was **inverted** on reopen, which made a workspace stow
  created quietly undeletable the second time it was opened — silently, because
  nothing exercised that path.
- The test helper used `t.TempDir()`, which **already exists**, so every
  "stow created" case was actually exercising adoption. A helper written for one
  purpose silently made another purpose untestable. `newOwnedStore` now exists
  specifically to arrange ownership, and says why.

`Destroy` is also **idempotent by success rather than by refusal**: a second call
returns nil, because a workspace that is gone is the state the caller asked for.
Reporting "there is nothing there" as an error would make a retry after a
partial failure impossible, and a caller that cannot retry a delete cannot clean
up reliably.

### 0.10 W4: liveness is established, never assumed

Added 2026-09-25, with the registry and TTL collector.

ADR 0009 §2 requires that collection "never" touch a live workspace, and adds
the hard part: the collector must be able to *establish* that a workspace is
unused, not assume it. Three designs were available and one was chosen:

| Design | Why not |
|---|---|
| A heartbeat the owner refreshes | A heartbeat is stale in exactly the direction that deletes somebody's work — a frozen process stops refreshing, and the collector concludes nobody is there. It is a guess with a timestamp on it |
| A recorded owner PID | PID reuse, and no way to tell a reused PID from the original. Needs a process start time, which is per-platform, to be safe at all |
| **An advisory file lock** | **Chosen.** The kernel releases it when the holding process dies, including on `SIGKILL` and an OOM kill. "Is anyone using this?" is answered by the operating system, and there is no stale state to clean up and no window in which a dead process still looks alive |

A second acquisition while one is held is refused rather than queued, and a
probe answers "not live" when no lock file exists — so the probe is
side-effect free and never leaves a lock behind that would block a real session.

Two refusals are the substance of the feature, and their order is the design:

1. **Liveness first.** A workspace that is merely *old* is never removed while
   somebody is in it.
2. **Ownership second.** An *adopted* workspace is never removed, ever, by a
   sweep. It is checked before the age check precisely because the failure would
   be catastrophic rather than merely wrong: adopting a directory is a documented
   feature, so an age-based sweep that could delete one would be reachable by
   waiting.
3. **Age last**, and only for workspaces this machine created, with a TTL that
   was explicitly recorded. An absent TTL means "no opinion" and is never
   treated as expired.

On Windows the standard library has no advisory file lock, and adding a
dependency for one is not worth it. Rather than fall back to a heartbeat — the
option this design exists to avoid — `Collect` refuses to run and says so. That
is the same posture as the parent-death watch's `ErrUnsupported`: a workspace
that cannot be reasoned about safely is left alone.

`Collect` reports **every** decision, not only the removals, because "nothing
was collected" and "three were skipped because they are in use" are different
answers and a caller diagnosing a leaked workspace has to tell them apart.

Worth recording: the TTL was only testable once the clock became injectable.
`Touch` originally used the wall clock, so the behaviour could have been
verified only by sleeping for an hour — and the first version of the test
asserted that a touched workspace was *never* collected, which is wrong. Touch
postpones; it does not exempt.

## 1. Product outcome

> **Amended by revision 2.** The outcome is a bounded artifact workspace, not a
> disposable S3 endpoint. The flow below now reads workspace-first with S3 as
> an opt-in capability, and the cleanup guarantee is bounded durability rather
> than deletion on scope exit. See ADR 0007 and ADR 0009.

Stow should become the shortest path from an agent or test to a bounded
workspace that is already its working directory:

```text
install package -> open workspace -> work in the directory -> optionally speak S3 -> close
```

A successful workspace should provide:

- a real directory the caller can `cd` into, with an automatically provisioned
  bucket over the same bytes;
- a configured S3 client for the language in use, when S3 compatibility is
  wanted;
- a loopback endpoint when S3 wire compatibility is required;
- isolated credentials and data;
- bounded durability: the workspace outlives the process and is collected on a
  TTL, and closing it is not deletion;
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
3. **Bounded durability by default.** The standard workspace survives its
   process and is collected on a TTL, measured in hours, rather than being
   deleted when a scope exits. It still cannot reach anything upstream
   without an explicit opt-in. *(Superseded by ADR 0007 and ADR 0009; the
   previous wording was "ephemeral by default", which was right for a unit
   test and wrong as the only mode.)*
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

> **Superseded in part.** Section 4.1 remains the documented architecture of
> the S3 session profile, which is supported and unchanged. It is no longer the
> product default: ADR 0007 makes the workspace the default and the S3 endpoint
> an opt-in facade. Section 4.2 is reversed for the same reason. Read this
> section as the contract for callers who want a real S3 endpoint.

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

> **Reversed.** Under ADR 0007 the direct runtime is the *default* wherever a
> client can embed it, and the child process is the fallback for clients that
> cannot. The text below is kept because it correctly describes when the
> embedded path is the better choice, and because the split it draws between
> "direct object interface" and "S3 wire surface" still holds inside each
> profile.

The direct Go/WASM runtime remains valuable for callers that:

- already run in the same process;
- do not need S3 wire compatibility;
- want zero listeners and credentials;
- want direct quotas and reset semantics.

`EmbeddedStow` should not pretend to be an S3 server. The in-process S3
compatibility adapter now exists for the native path as `runtime.StoreAdapter`,
which is what gives the HTTP server a single choke point for quotas and
accounting. The public `EmbeddedStow`/`@chester-hill-solutions/stow-s3/browser` profiles remain
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

> **Re-sequenced by revision 2.** Sections 9.1–9.5 below are the current plan.
> The historical phases that follow are kept as the record of the S3 session
> path, which is supported and largely executed. They are not the order of
> work; 10.1 is the backlog and 9.1 is the shape of it.

### 9.1 Shape of the plan

Five movements. Revision 3 adds one and promotes another, because the market
analysis in section 0.7 changed what the sequence is *for*.

```text
  Phase 0        Phase A          Phase B            Phase C         Phase D
  THE GATE       the workspace    durability         compatibility   reach
  ─────────      ───────────      ───────────        ────────────    ────────────
  install works  W0 backend       W3 lifecycle       W2 virtual-host W7  conform to
  W14 accounts   W1 pkg/stow      W4 registry + GC   W10 error codes      owned seams
  W15 proof      contract tests   W8 handoff         W6 quotas        W13 doc gate
                                                                W16 displace
                                                                W17 MCP
```

**Phase 0 is first because it is the strategy.** Section 0.7 argues the wedge is
a distribution claim, and a distribution claim that cannot be `pip install`ed is
not a distribution claim. This phase was previously a parallel note about
accounts; it is now the gate the rest waits on, because everything after it is
worthless if a competitor ships the same wedge first with a working installer.

Phases A and C have no dependency on each other. Phase B depends on A. Phase D
depends on B, and W12 depends on nothing.

### 9.2 Phase 0 — the gate: it installs, or nothing else matters

**Entry:** nothing. No code dependencies; start immediately, in parallel with
Phase A.
**Exit:** `pip install stow-s3` and `npm install @chester-hill-solutions/stow-s3`
both work on a machine with no account, no card, and no registry configuration,
on every platform in the support matrix, with the binary inside.

The work splits by who owns it, and the split matters:

| Item | Owner | Why it is not an engineering task |
|---|---|---|
| Organisation, npm trusted publishing, PyPI trusted publisher | **The owner** | Accounts. No code change shortens this |
| Registry line in the documented install command | Engineering | The package must be reachable where a consumer actually looks |
| Per-platform wheel tags and the executable bit | Engineering, done | Two packaging traps already recorded, both invisible until installed for real |
| W15 clean-install proof | Engineering | The gate that keeps Phase 0 honest |

**W15 is the part that is easy to skip and must not be.** A release gate that
asserts publication status from a declaration in a script is a status table, and
this repository has three recorded instances of a status table disagreeing with
reality. The proof is a clean-room install — empty container, no credentials in
the environment, no npmrc — that runs an actual agent-shaped workload. It runs in
CI on every release, and it fails on a 404.

Note the trap already recorded about local verification: the scope is bound to a
private registry in one developer's npmrc, so `npm view` answers for the wrong
registry and reports a package a consumer cannot install. The gate must query
registries over HTTP and read status codes, not ask npm.

### 9.3 Phase A — the workspace backend

**Entry:** the contract in `docs/workspace-contract.md` is agreed.
**Exit:** WS-01 and WS-02 pass, and the rest of that document's section 8 corpus
runs.

**Status: complete.** The backend is `internal/storage/workspace` and the public
entry point is `stow.OpenWorkspace`. Both same-bytes directions pass, the store
runs the shared backend contract suite, and the package is at 74% coverage.

The discipline that made it trustworthy is worth keeping: WS-01 and WS-02 were
written before the backend and confirmed to fail against a store that cannot
satisfy them. Section 0.6 records three other instances in this repository of a
green suite over a path that had never run.

### 9.4 Phase B — durability

**Entry:** Phase A complete.
**Exit:** a workspace survives its process, resumes by ID, and is collected on a
TTL without ever touching a live one.

The uncomfortable part of this phase is that it turns a deliberate breaking
change into a failing test. `close()` stops meaning "the data is gone", so the
"100 sessions leak nothing" assertion is wrong the moment the phase starts.
Replacing it in the same diff as the behaviour change, with the message saying
why, is the discipline. A test quietly edited to match new behaviour teaches the
next reader that the old guarantee was optional.

This phase is also where requirement 5's honest half lands: a handoff that names
a workspace rather than a secret key.

### 9.5 Phase C — compatibility and quotas

Phase C finishes a contract that is already 95% written: two entries in the
unsupported-marker list, virtual-hosted style end to end, and the quota
dimensions that do not exist, including making the request-body cap raisable.
It is small, it is independent, and it should not queue behind Phase A.
Requirement 6 of the product direction is *specifically* about error codes SDKs
already understand, and two of them are currently wrong.

### 9.6 Phase D — reach, by conformance rather than invention

Phase D makes Stow reachable, and revision 3 changes its content entirely. The
original plan was to build integrations so that `Agent(workspace=...)` was Stow.
That seam is owned by a framework vendor and a Kubernetes SIG, and their word
for this is a *mount*.

So Phase D is:

- **W7**, rewritten: implement the storage-mount half of an existing sandbox
  manifest, and the filesystem capability of the Kubernetes SIG API. The success
  condition is unchanged and still right — a third party using Stow without
  importing it — but it is reached by satisfying someone else's contract.
- **W16**, the displacement: publish the zero-account replacement for what CI
  lost on 2026-03-23, aimed at the teams whose pipelines broke that day. A dated,
  addressable, still-fresh event, and the most time-sensitive positioning
  available.
- **W17**, the MCP server, still the honest boring-tools surface.
- **W13**, the doc-shape gate, so the install surface stops teaching a contract
  that no longer exists.

### 9.7 Historical phases: the S3 session path

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

### 10.1 Revision 3 backlog

Revised by revision 2 (ADR 0007/0008/0009) and again by revision 3, which
re-derived the sequence from the market position in section 0.7. Supersedes the
ordering in 10.2 for anything on the workspace path.

Three facts set the order. **The public in-process runtime could not hold a
workspace** — W0/W1, now done. **The subsystem promote would depend on has never
worked** (section 0.6, defect 1). And **the wedge is a distribution claim**, so
Phase 0 gates the rest rather than running beside it.

| ID | Pri | Work item | Depends on | Exit condition |
|---|---:|---|---|---|
| W14 | **P0** | The accounts: organisation, npm trusted publishing, PyPI trusted publisher, and a registry line in the documented install command | — | **Owner action, not engineering.** Both packages install from a clean machine with no account and no registry configuration |
| W15 | **P0** | Clean-install proof in CI | W14 | An empty container, no credentials, no npmrc, runs a real agent-shaped workload through the published package. Fails on a 404. Queries registries over HTTP, never via `npm view` |
| W0 | P0 | Workspace backend | — | **Done.** Both same-bytes directions pass; 74% covered; runs the shared backend contract suite |
| W1 | P0 | Expose it through `pkg/stow` | W0 | **Done.** `stow.OpenWorkspace`, in-process, no injected store; `pkg/stow` at 76% |
| W3 | P0 | `Destroy`, and the close/destroy split | W1 | **Go done.** `stow.Workspace.Destroy` removes the directory and refuses one stow *adopted*. The TypeScript and Python **sessions** are deliberately unchanged — see the correction below |
| W4 | P0 | Session registry and TTL collector | W3 | **Go done.** A dead session's workspace is reclaimed; a live one, and an adopted one, never are. Liveness is an OS-held advisory lock, not a guess — see §0.10 |
| W2 | P1 | Finish the S3 compatibility contract | — | Virtual-hosted style runs in the corpus and the client stops hardcoding `forcePathStyle` |
| W10 | P1 | Error codes SDKs already understand | W2 | `versions` and `location` return `NotImplemented` (0.6 defect 4) |
| W6 | P1 | Quotas a host can actually set | W1 | Bytes, objects, request rate, wall clock, audit log, and a **raisable** `MaxRequestBytes` (0.6 defect 3) |
| W5 | P1 | S3 facade on the same runtime instance | W1 | One object store, two surfaces. A facade with its own storage is rejected in review |
| W8 | P1 | Handoff reference | W4 | A handoff carries a session ID and an open capability, not a secret key (`session.ts:253`) |
| W7 | P1 | **Conformance to owned seams** — the storage-mount half of a published sandbox manifest, and the filesystem capability of the Kubernetes SIG agent-sandbox API | W5 | A third party obtains a Stow-backed workspace **through someone else's interface**, without importing Stow. The original goal, reached by satisfying a contract we do not define |
| W16 | P1 | The displacement | W14, W15 | The zero-account replacement for what CI lost on 2026-03-23 is published and installable, aimed at the teams whose pipelines broke that day |
| W17 | P2 | MCP stdio server | W3 | The boring complete six: `write_file`, `read_file`, `list`, `stat`, `put_url`, `presign_download` |
| W9 | P2 | **Promote, after the read/write path is fixed** | 0.6 defect 1, W4 | Blocked, not merely late. Run-through has never moved a byte |
| W11 | P2 | Platform cost baseline: embed vs child, per language | W1, W5 | Two profiles reported separately, never averaged. macOS arm64 and Windows **measured on their own runners** |
| W12 | P2 | Windows parent-death watch and a build tag on its test | — | The test compiles on Windows; the watch is implemented or explicitly refused. Requirement 1 is untrue on Windows until this lands |
| W13 | P1 | A gate for the documented default | W3 | The four user-facing docs describe the current default, declared once in a script. **Blocked on fixing the inverted disclaimer rule first** (0.6 defect 9) — do not extend a rule whose test is backwards |

The critical path, with Phase 0 promoted:

```text
W14 -> W15 ─┐
            ├─> W16 (the strategy lands)
W0 -> W1 -> W3 -> W4 -> W8
            └-> W5 -> W7
W2, W10, W12: independent, from day one
W6, W13, W17: after their dependencies
W9: blocked on fixing run-through, not on anything above
```

**W14 is the only item on this list that is not engineering, and it gates the
most.** It has a lead time no code change shortens. Everything else is worthless
if someone else ships this wedge first with a working installer — which, given
that the two previous occupants of this exact position left inside five weeks of
each other, is not a hypothetical.

### 10.2 Prior backlog: the S3 session path

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

#### Workspace path

Added by revision 2. The tests above describe the S3 session; the workspace
needs its own, and the difference is that a workspace is a *directory*.

**Written before the implementation.** WS-01 and WS-02 are the first two cases
written in Phase A and the first two run. They must be confirmed RED against
`internal/storage/fs`, which is a store rather than a workspace, before the
backend exists. A test that has never failed has not been tested.

**Corpus, not fixtures.** All fourteen cases in
`docs/workspace-contract.md` section 8 run through `conformance/corpus/cases.json`
against the workspace backend alongside the memory and filesystem backends. A
hand-written expectation for a workspace is a test that can be satisfied by
whatever the implementation happens to do.

**The harness default is the broken case.** A workspace harness creates no
buckets, pre-seeds nothing, and leaves the directory empty unless a case asks
otherwise. This is the direct lesson of the run-through suite, where 13 of 19
adapter constructions passed the same store as both local and cache and the six
that did not pre-seeded the cache's buckets, which made the defect unreachable
in every one of them.

**Adoption is tested by not doing anything.** WS-03, WS-08, and WS-12 all
begin by writing a file with `os.WriteFile` and then asking stow to serve it.
There is no import call in any of them, because there is no import call in the
product.

**Two keys, one file.** WS-06 runs on a real case-insensitive filesystem. A
macOS or Windows runner is required; a cross-compile cannot observe the
collision, and a test that skips on the platform where the bug lives is the
`kevent` defect's second cousin.

**Platform matrix.** Linux x64, macOS arm64, and Windows, each on its own
runner, for: key encoding (WS-05, WS-13), case folding (WS-06), path length
(WS-04), and the atomic manifest write under concurrent writers.

**Cost, reported per path.** The embedded workspace path and the child-process
session path are measured and published separately, on the same machine, with
the backend named. A single number covering both is a number that describes
neither. Windows and macOS numbers are measured, not extrapolated from Linux.

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

### 14.1 Risks specific to the workspace path

Added by revision 2. The risks in 14.2 predate it. Revision 3 adds the market
risks, which are the ones that can end the project rather than slow it.

| Risk | Mitigation |
|---|---|
| **The concept was taken, and the differentiator is thinner than the plan assumed** | Section 0.7 states the wedge as a *distribution* claim and demotes every concept claim. The build is not invalidated: an in-process runtime with no listener, port, credential, or process is exactly what "starts with nothing" requires |
| **A competitor ships this wedge with a working installer first** | W14 and W15 are promoted to a gate for the whole strategy. This is what makes the account work urgent rather than merely outstanding |
| **The embed seam is owned by others and the win condition shrank** | W7 is rewritten as conformance. The goal is unchanged — a third party uses Stow without importing it — but it is reached by satisfying a contract we do not define, in their vocabulary (a *mount*, not a *workspace*) |
| **A hosted provider offers a usable no-card free tier** | Would narrow the wedge to "no Docker" and strip it of structural character. Listed in `docs/competitive-landscape.md` §10.6 as a verification task, because it is cheap to check and expensive to discover late |
| **The MinIO framing is wrong because a maintained fork exists** | The plan asserts only what the repository states: unmaintained, source-only, commercial successor. It does not say "MinIO is dead", and the fork is recorded as the strongest available mitigation |
| A structural finding is graded on secondary evidence and is wrong | Every market claim in Part 2 carries an A/B/C grade, and the single most load-bearing one is grade C. §10.6 lists what to read before the roadmap is approved |
| The same-bytes claim is tested after the fact, so it confirms the implementation rather than the claim | WS-01 and WS-02 were written first and confirmed to fail against a store that is not a workspace. Section 0.6's harness rules bind here, and are not advisory |
| Two S3 keys collide on a case-insensitive filesystem and one is silently lost | The backend indexes by case-folded path and escapes the loser. The rule set is the *union* of all supported hosts, so it is testable on Linux CI rather than only on a Windows runner |
| The collector deletes a live agent's working directory | `workspace_in_use` is a correctness error, not policy. The collector must be able to *establish* that a workspace is live, not assume it |
| `close()` stops deleting and callers never call `destroy` | Bounded by a TTL, reported through metrics and the capability query, and named in the changelog as a breaking change. A forgotten workspace is a disk cost, not a data-loss incident |
| A corrupt manifest reads as total data loss | The filesystem, not the manifest, is the source of truth for existence, and a corrupt manifest refuses the open rather than rebuilding empty. WS-09 |
| The workspace backend is a third store and repeats the two-store adapter bug | Every layering adapter over it gets the `cache_policy.go:139` treatment — both missing sentinels — and a test comparing what the tests pass against what `main` passes |
| A cost figure from the embedded path is quoted as a general session cost | Two profiles reported separately, never averaged, and the Python exception stated in ADR 0007 section 3 rather than discovered by a user |
| The install surface teaches the old contract | W13 declares the current default once in a script, gated on first fixing the inverted disclaimer rule (0.6 defect 9) |
| Windows support is claimed from a cross-compile | W11 and W12 require runners. The kqueue parent-death watch shipped as fixed once, after being cross-compiled, vetted, and described as fixed in a commit message |

### 14.2 Risks from the S3 session plan

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

### 15.1 Added by revision 3: things the market analysis rules out

These are new non-goals and the direct consequence of section 0.7. Each is
something a fair reading of the original brief would have put in scope, and each
is now explicitly not:

- **A sandbox runtime.** Do not build isolation, microVMs, gVisor, or container
  management. Seven hosted providers and two standards bodies already do it, and
  it is the opposite of the zero-infrastructure wedge. Stow's safety property is
  *no path to production credentials*, not *untrusted code execution*.
- **A FUSE or kernel filesystem mount.** It is the approach the incumbent
  cloud product was positioned against, it needs privileges a library should not
  ask for, and a workspace is already a real directory. The S3 facade is the
  integration point; a mount would be a second one.
- **A hosted relay for human download.** Still out of scope per ADR 0009
  section 6, and revision 3 strengthens the reason: a relay is an operating cost
  and a second product.
- **A vector store, memory graph, or tool marketplace.** Unchanged from the
  brief's own reasoning — a different primitive.
- **A dashboard.** Unchanged, and now also uncompetitive.
- **Chasing the framework embed as a moat.** We can be a provider behind an
  owned interface. We cannot be the owner of it, and a plan that assumes
  otherwise is planning to lose.
- **Multi-region, multi-tenant, or cross-machine durable workspaces.** The wedge
  is a local disposable workspace. Bounded durability on one machine is the
  design, and it is the feature.

## 16. First execution slice

> **Superseded by revision 2.** The slice below was written against a default
> of a scoped child S3 session, and it has been substantially executed: the
> request-body cap, the quota wiring, the versioned ready protocol, both
> language clients, the platform binary packaging, and the benchmark baseline
> all exist. What it did not question was the default itself. The slice below
> remains the record of that session's work; the slice that follows replaces
> it as the next one.

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

### 16.1 The slice that replaces it

The last sentence above is now the wrong target. "Nothing survives the block"
was correct when the block was the unit of work; it is wrong when the block is
a framework's per-task callback, which is what ADR 0009 replaces it with. The
replacement sentence is the one in ADR 0007 section 5: an agent runtime starts
a bounded workspace that is already its working directory, and the same bytes
are reachable through S3.

Three tasks, in this order, and nothing else starts until they are done:

1. **W0 — the workspace backend.** Objects as real files, one manifest,
   adopting files it did not write. Its exit condition is the two-direction
   same-bytes test, in the conformance corpus rather than in a unit test that
   a mock could satisfy. Nothing else in this plan is load-bearing.
2. **W1 — expose it through `pkg/stow`.** A persistent in-process workspace
   with no injected store and no child process. Until this exists, the
   direction in ADR 0007 is a design and not a product.
3. **W2 — finish virtual-hosted style.** Independent, cheap, and the last
   uncompleted item in the S3 compatibility contract.

The account-side unblock for requirement 1 runs alongside all three, because
it is waiting on people rather than on code.

## 17. Decision checklist before implementation

Answered by revision 2 unless marked open. An unanswered box next to a decided
question is how this document drifted from the code twice already.

- [x] What is the exact default promise? — **A bounded workspace that is already
  the working directory, with S3 as an opt-in facade.** ADR 0007; the interface
  is specified in `docs/workspace-contract.md` section 1. The S3 session
  profile remains supported under ADR 0004.
- [ ] What is the canonical session name in TypeScript and Python? — Open. The
  workspace introduces a second noun, and whether the client exports
  `openWorkspace` alongside `openStow` or replaces it is a naming decision
  nobody has made. Note that `stow` is taken as a bare name on npm, PyPI, and
  in `PATH`, so the namespace is not free.
- [x] What is the PyPI distribution name, and is it available? — **`stow-s3`**, import
  package `stow_s3`, Python 3.10+. `stow` is taken on PyPI by an unrelated
  package. Recorded in `docs/distribution-spike.md`. The name is reserved and
  the project is not created; confirmed as a 404 from outside the repo.
- [ ] Which S3 client libraries are first-class?
- [ ] Is async Python part of the first release or a later release? — Open, and
  now heavier: Python has no embedded path at all (ADR 0007 section 3), so async
  would be layered on a subprocess.
- [x] Which platforms ship in the first release? — **macOS arm64 and Linux x64**
  for the S3 session profile. **For the workspace backend, Windows is
  first-release too**, because the key-to-path encoding in
  `docs/workspace-contract.md` section 3 has Windows-specific rules that are
  untestable without a Windows runner (W12, WS-13). The release pipeline already
  builds all four candidate platforms.
- [x] How is the binary distributed with each package? — **Platform optional
  packages**, resolved before `PATH`, so a plain install starts a session with
  no environment setup. Measured in `docs/distribution-spike.md`. Note that
  `STOW_BIN` is the *from-source* key and disappears once anything is
  published.
- [x] What are the default quotas? — **16 MiB / 1,000 objects / 8 MiB per
  request** for a session. The byte and object counts are enforced natively. The
  memory figure that came with them has been restated: sessions now run
  `GOGC=50`, which measures 3.27 MB peak RSS per MiB rather than 4.59, so a
  16 MiB session implies roughly 65 MB including fixed process RSS, not 85 MB.
  See section 11 and `docs/benchmarks/session-baseline.md`. **The 8 MiB request
  cap is currently unraisable** — it is a constant with no flag (section 0.6,
  defect 3) — and must become host-configurable in W6.
- [x] Which capabilities must be present for an agent adapter? —
  `docs/workspace-contract.md` section 6. Notably `caseInsensitiveHost`, which
  changes observable behaviour, and `s3Session: false`, which stops a caller
  assuming the workspace profile's guarantees.
- [x] How are child-agent credentials handed off? — **Two separate values.** An
  in-process-tree environment mapping, which does carry the generated secret key
  and already exists, and a *handoff reference* that carries a session ID and an
  open capability and no secret. ADR 0009 section 5.
- [x] How are cleanup errors reported without hiding the primary error? —
  `withStow` already aggregates rather than masking (ADR 0004 section 2). The
  workspace contract adds `workspace_in_use` as a correctness error and
  `workspace_manifest_corrupt` as a refusal that deletes nothing.
  `docs/workspace-contract.md` section 5.
- [ ] Which run-through behaviors are safe enough for the first release? —
  Open, and the question is currently unanswerable: run-through has never read
  through to an upstream or propagated a write (section 0.6, defect 1). Nothing
  in run-through should be advertised for the workspace path until that is
  fixed, and promote is blocked on it.
- [ ] Which external pilots define success? — Open. The measurable form is
  already written down in section 1: installation success, time to first
  operation, repeated use, reliability under parallel sessions, and the number
  of external workloads using the published packages without tribal knowledge.
  What is missing is the named pilots.
