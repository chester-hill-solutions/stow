---
status: accepted
supersedes: 0002 (section 5, only as to what constitutes live-write consent)
---

# A policy is not consent to write upstream

This ADR narrows one decision in
[ADR 0002: SDK compatibility and mirror-writes](0002-sdk-compatibility-and-mirror-writes.md)
and leaves the rest of it, and all of
[ADR 0001: auto-detect run-through mode](0001-auto-detect-run-through.md), in
force. It does not change mode detection: a developer with upstream credentials
in their environment still gets run-through by default, and that remains a
deliberate trade-off recorded in ADR 0001.

## Context

ADR 0002 section 2 required that "live upstream writes require an explicit
policy or flag opt-in", and section 5 described `mirrorWrites` as itself being
an explicit opt-in that "prints a prominent startup warning". The implementation
took the first of those literally. `ConfigFromEnv` contained:

```go
if cfg.Policy == PolicyMirrorWrites {
    if _, ok := os.LookupEnv("STOW_ALLOW_LIVE_WRITES"); !ok {
        cfg.AllowLiveWrites = true
    }
}
```

The policy therefore granted consent by default, and the absence of the env var
was what enabled writes.

Combined with ADR 0001's auto-detect default, that produced a plausible chain to
a real data-loss event with no deliberate act by the developer:

1. A `.env` copied from a staging machine sits in the project root. It contains
   `STOW_POLICY=mirrorWrites` and a `STOW_ENDPOINT` with credentials for a
   bucket several people write to.
2. `Stow.start()` is called with no options, which is how the README and the
   existing tests call it.
3. Auto-detect finds the credentials and selects run-through.
4. The policy grants live-write consent on its own.
5. Every mutation the application performs during the test run is propagated to
   a shared bucket.

Nothing in that chain required the developer to decide anything. The banner did
warn, but a warning printed at startup is not consent, and the write is not
recoverable once the outbox has propagated it.

Two supporting facts made this worse than a single bad line. The effective write
policy was derived independently in three places — the startup banner, the
server status payload, and `validateLiveWriteBackend` — so the banner could
report `mirrorWrites` while the adapter refused the write, and no single test
covered the pair. And the live conformance tests, which are the only ones that
exercise propagation, are correctly gated behind explicit opt-in environment
variables, so the unsafe path had no coverage by construction.

## Decision

**A policy is a routing choice. The live-write flag is the consent. Neither
implies the other.**

- `STOW_POLICY=mirrorWrites` alone never enables propagation. Writes stay local.
- `STOW_ALLOW_LIVE_WRITES=true`, or `--allow-live-writes`, is required and is
  sufficient on its own, under either policy.
- An explicit `STOW_ALLOW_LIVE_WRITES=false` is a refusal, not an omission, and
  is never overridden by the policy.

The three independent derivations of the effective write policy are replaced by
one exported function, `runthrough.EffectiveWritePolicy`, which is what the
banner, the status payload, and startup validation all read. The reported policy
distinguishes `mirrorWrites-disabled` from `mirrorWrites` so that a configured
but inert policy is visible rather than silently described as active.

`validateLiveWriteBackend` now keys on consent rather than on policy, so a
run-through server on the memory backend with `mirrorWrites` configured and no
consent is no longer refused. Under this decision it performs no propagation, so
requiring the filesystem backend for it was refusing a configuration that works.

## Why not change the mode default

The 10-plan proposes that `Stow.start()` pass `--mode local` unless run-through
is requested. That reverses ADR 0001 and would break every existing run-through
user who relies on their existing AWS and S3 environment variables. It is a
larger change than the defect requires, and it is recorded as an open question
rather than taken here.

The defence in depth that the mode default was supposed to provide is added
instead, and asserted rather than asserted-by-comment: `buildStore` is the single
function that constructs a store, and in local mode it never constructs an
upstream client. A local server therefore holds no object capable of reaching a
provider, regardless of environment, policy, or consent. `cmd/stow-s3`'s
`TestLocalModeMakesZeroUpstreamRequests` sets the full hazardous environment,
forces local mode, exercises create/put/get/head/list/delete against a recording
upstream, and requires zero hits.

Both layers are asserted because either alone is insufficient. Relying on the
consent flag alone rests the whole defense on one boolean being read correctly;
relying on the store wiring alone would let the consent-flag misread survive
unnoticed, which is the bug this ADR exists to close.

## Consequences

- `STOW_POLICY=mirrorWrites` without `STOW_ALLOW_LIVE_WRITES=true` no longer
  propagates. This is a behavior change for anyone relying on the old implicit
  grant, and it is a narrowing of capability rather than a break: their reads
  still go through run-through, and their writes still succeed locally.
- Anyone who genuinely wants propagation sets the flag, which they must now do
  deliberately. The banner says so on every run-through start, and the hint
  names the exact variable.
- A run-through server on the memory backend with a configured-but-inert
  `mirrorWrites` policy now starts instead of failing. This is the intended
  effect of separating routing from consent.
- The second fix in this release, the owned data-directory reset, is recorded in
  the same release because it is the same class of defect: a destructive
  operation whose default was more permissive than its documentation implied.
  See ADR 0006.

## Considered Options

- **Make the default local.** Rejected here, for the reasons above. It remains
  the strongest long-term option and should be decided as a 1.0 breaking-change
  proposal with a migration note, not as a side effect of a safety fix.
- **Keep the grant and require `STOW_ALLOW_LIVE_WRITES` to be unset, as today.**
  Rejected. An unset variable is not a decision, and this makes the *absence* of
  a variable the enabling condition, which is the most dangerous shape the
  default could take.
- **Prompt interactively.** Rejected. A backgrounded process, a test run, or a
  CI job cannot answer, and cannot be made to fail safely if it does.
- **Drop `mirrorWrites` entirely.** Rejected. Read-through plus explicit
  propagation is a real need; only the implicit grant is the problem.
