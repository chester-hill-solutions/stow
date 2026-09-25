---
status: accepted
---

# A workspace outlives the process that created it

This ADR decides the lifecycle of the workspace default in ADR 0007. It
amends ADR 0004 sections 1 and 2, which promised that a session is "disposed
when the scope exits" and that closing it releases the temporary directory.
Those remain true of the child-process session. They are no longer true of a
workspace, and the difference is deliberate.

## Context

ADR 0004's default is correct for a unit test and wrong as the only mode. An
agent process is crash-prone, preempted, and resumed; treating its scratch
directory as a child process's property means the work is lost exactly when
it was most expensive to produce.

The cost of durability here is state on the operator's disk and a component
that can delete the wrong thing. Both are acceptable; neither is free, and
the defaults have to be conservative enough that a forgotten workspace is a
disk cost rather than a data-loss incident.

## Decisions

### 1. A workspace has an identity that outlives its process

A session ID, minted at creation, names a durable record: directory, bucket,
quotas, creation and last-used times, and liveness. Resuming is opening by
ID. It is not reconstructing state from an environment mapping, and a
resumed workspace is the same workspace with the same objects, not a copy.

### 2. Retention is bounded by default, and collection only touches dead sessions

A TTL measured in hours, not forever, swept by a collector owned by the
runtime. A workspace is collectable only when no live session refers to it.
"Live" includes being a process's current working directory, and the
collector must be able to establish that rather than assume it.

Collection never runs against a workspace a live session holds. This is the
one invariant in this ADR that is a correctness property rather than a
policy, because the workspace is a working directory: deleting it out from
under a running agent destroys the artifact it is producing.

### 3. `close()` releases the process. `destroy()` deletes the bytes

This is the direct contradiction with ADR 0004 section 2 and it is stated
rather than buried. Closing a session releases the client and, for a child
process, the process — and marks the workspace for collection. It does not
imply the data is gone.

Both language clients get both operations under the same names, because a
lifecycle that only offers deletion is the reason people keep using temp
directories. A caller who wants the old behaviour gets it by asking for it.

### 4. Promotion is explicit, always, and it is not built yet

Moving a workspace to a real bucket is a call with a named destination. It is
never a side effect of expiry, of close, or of a run that happened to
succeed. There is no configuration in which a workspace becomes a live write
by accident.

Promotion is a live write and carries the same consent requirement as any
other: the opt-in that ADR 0005 introduced, and not a policy flag. A caller
who has not opted in gets a local copy and a refusal, not a partial upload.

**The contract above is decided; the implementation is blocked.** Run-through
mode has never read through to an upstream or propagated a write: the
cache-miss branch is unreachable and a quota pre-check fails the write before
the outbox is reached (`docs/agent-dx-plan.md` section 0.6, defect 1). Promote
is therefore sequenced after that is fixed, not merely after a workspace
exists. Splitting it from the handoff reference (section 5) is deliberate:
the handoff is local and unblocked, and the two must not share a release.

### 5. A handoff is a reference, not a credential

Agent A writes, agent B or a person reads. The handoff carries the session
ID and the capability to open that workspace — not the secret key.

Today `handoff()` returns `AWS_SECRET_ACCESS_KEY` in an environment mapping
(`packages/stow-s3/src/session.ts:253`). That is defensible inside one
machine's process tree and wrong for the thing this ADR is for, which is a
value a person pastes into a conversation. The existing mapping stays
available for the in-process-tree case under its current name; the handoff
reference is a separate value with a separate lifetime.

### 6. No shared daemon, no network registry

The registry is per-workspace state on the operator's own disk, owned by the
runtime that created it. This keeps ADR 0004 section 4.3's rejection of a
shared daemon intact: there is no session routing, lease, or heartbeat
problem to solve, because there is no second process to route between.

The honest cost is that a workspace is reachable only from the machine that
created it. Requirement 5 of the product direction — a download that works on
the operator's own machine — is satisfied by that, and a relay is explicitly
out of scope here.

## Consequences

- The existing lifecycle test expectation changes. "100 sessions leak
  nothing" is no longer the contract; it becomes "100 sessions leave 100
  reclaimable workspaces and zero live processes", plus a test that the
  collector reclaims them once their sessions are gone. This is a deliberate
  change to a passing test, and the diff should say so.
- A forgotten workspace is a disk cost, bounded by the default TTL and the
  session's byte quota. Both are reported by `/_stow/metrics` and by the
  client's capability query, so a host can see what it is holding.
- `close()` becoming non-destructive is a behaviour change for anyone reading
  ADR 0004. It is a major-version change for the client packages, and the
  changelog entry has to say that a session's data now outlives it.
- Resume is only meaningful for a persistent backend, so it is blocked
  behind ADR 0008 in the same way the workspace default is.
