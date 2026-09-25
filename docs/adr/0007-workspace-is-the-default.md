---
status: accepted
---

# The workspace is the default; S3 is an opt-in facade

This ADR changes the default agent-facing surface. It supersedes ADR 0004
section 1 ("the default promise") and `docs/agent-dx-plan.md` section 4.1
("Default implementation: managed child S3 endpoint") for the default path
only. ADR 0004 continues to govern the child-process session, which stays
supported and unchanged.

Measurements referenced here are from `docs/benchmarks/session-baseline.md`.

## Context

The default agent surface is currently a short-lived child server: spawn
`stow-s3 serve`, hand back an S3 client, delete the data directory on close.
ADR 0004 chose that shape deliberately, for S3 wire compatibility and
process-level fault isolation, and it works.

Two measured facts make it the wrong *default* now.

First, cost. A session pays 11.6 MB of fixed process RSS, 15.4 ms p50 to
ready, and 35.5 ms p50 shutdown before a single byte is stored. That is the
right cost for a service you deliberately started and the wrong cost for
something a framework starts on every task. The measured per-session byte
cost has already been fought down twice, from 4.59 MB per MiB to 3.27 MB by
tightening the collector, and the fixed cost is untouched by that work.

Second, and more decisive: the data directory is not a directory. Objects are
stored as base64 inside a per-object JSON record
(`internal/storage/fs/fs_records.go:14`), so an agent cannot read a file it
was told to read, cannot `cd` anywhere useful, and cannot leave an artifact
that a person will later open. A developer who wants real files creates a
temp directory first and runs Stow beside it. That makes Stow a sidecar to
the thing it is supposed to be.

The product direction that follows is that one call returns a bounded
artifact workspace that is already the working directory, and that speaking
S3 is something such a workspace *can* do rather than what it *is*.

## Decisions

### 1. The workspace is the default; the endpoint is opt-in

One call returns a workspace: a real directory, a bucket, and an identity
that outlives the process that created it. Whether that workspace also
exposes a loopback S3 endpoint is a second, separate decision made by the
caller.

The current default makes those the same decision, which is why a
non-S3-shaped agent workload pays for S3 compatibility it never uses.

### 2. Where a client can embed the runtime, the default does not spawn a process

`pkg/stow` already provides an in-process runtime with no listener, no
credentials, and no environment dependency. A Go or embedded-WASM caller
binds that runtime to a workspace directory and gets the product with no
process, no port, no credential, and no `close()` that has to survive
`SIGKILL` — because there is no process to orphan.

The child-process session remains the correct shape for a caller that needs
a real S3 endpoint from a client that cannot embed, and it is not
deprecated. `docs/benchmarks/session-baseline.md` covers both, and the two
cost profiles must be reported separately rather than averaged.

### 3. Python is the honest exception, and it is decided here rather than discovered later

There is no embedded path for Python today; `packages/stow-s3-py` spawns a
server. The in-process default therefore does **not** hold for `boto3`
callers.

This is acceptable because the *workspace* is the product and the process is
an implementation detail that differs by language. A Python caller gets the
same directory, the same bucket, the same quotas, and the same bytes through
S3 — it pays process cost for them. Shipping a CPython extension to remove
that cost is deferred, not assumed, and the packaging work it implies
(per-platform extension wheels) is a separate decision.

The consequence to state plainly: the cost target in
`docs/agent-dx-plan.md` section 11 is a target for the embedded path. It must
not be quoted as a general session cost.

### 4. The public in-process runtime needs a persistent backend before any of this is usable

`pkg/stow` exposes exactly one backend, `BackendMemory`
(`pkg/stow/types.go:10`), and `runtime.Open` rejects any other backend unless
a store is injected by an in-tree caller
(`internal/runtime/instance.go:64`). The public in-process runtime is
memory-only, as ADR 0003 stated.

A memory-only workspace cannot be a working directory, because nothing
survives the process. This is the load-bearing dependency for the entire
direction: it is new capability in the public runtime, not a refactor of
existing code, and every other item in this ADR is blocked behind it. The
backend itself is decided in ADR 0008.

**Landed.** The backend is `internal/storage/workspace` and the public entry
point is `stow.OpenWorkspace`, which returns a `*stow.Workspace` embedding the
runtime so quotas and accounting keep a single choke point. The workspace
backend is deliberately *not* reachable through `stow.Open`: a workspace needs a
directory and `Options` has nowhere to put one, so offering the constant on that
path would be a trap. `Open` refuses it with `ErrUnsupportedBackend` rather than
quietly handing back a memory runtime that would lose the bytes.

### 5. "Same bytes" is the acceptance gate, stated as a test

The claim in the product direction is that local files and `s3://` are the
same bytes. That is falsifiable, and the test does not exist in the tree
today:

- a file written through the agent's own filesystem tools, with Stow never
  involved, is returned by `GetObject` with the same bytes;
- an object written through `PutObject` exists on disk as a real file with
  the same bytes, at a path a person can open.

Both directions, or the workspace is not a workspace. A direction that
cannot be tested in two lines of a conformance case is a slogan, and this
repository has already shipped two permissive defaults that a passing gate
did not catch (ADR 0005, ADR 0006).

## Consequences

- The public Go runtime grows a second backend and a real persistence
  contract. That is the first thing to build.
- The default is no longer uniform across languages. That is a real cost
  against the "one obvious happy path" principle, paid deliberately in
  exchange for the workspace being available to every language.
- Existing `Stow.start()`, `Stow.connect()`, `withStow`, and `with_session`
  are unchanged and additive. Nothing in this ADR is a breaking change.
- A workspace that speaks S3 and a workspace that does not are the same
  object store, not two stores with a synchronization problem. Any design
  that gives the facade its own storage is rejected.
- Everything in `docs/agent-dx-plan.md` sections 3, 4, 10, and 16 that
  assumed an ephemeral child server by default is amended by
  `docs/agent-dx-plan.md` section 0.5.
