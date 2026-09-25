---
status: accepted
---

# A reset deletes only a data directory stow created

This ADR records the ownership rule for the data-directory reset. It is
recorded alongside [ADR 0005](0005-live-write-requires-explicit-consent.md)
because it is the same class of defect: a destructive operation whose behavior
was broader than its name and documentation implied.

## Context

`Stow.start()` accepted `cleanSlate?: boolean`. Its implementation was:

```ts
if (options.cleanSlate) {
  await rm(dataDir, { recursive: true, force: true });
}
```

`dataDir` defaulted to `.stow`, but the option accepted any string the caller
supplied, and nothing checked what that string pointed at. So:

```ts
await Stow.start({ dataDir: homedir(), cleanSlate: true });
```

recursively deleted the developer's home directory. `dataDir: ".."` from a
project directory deleted the parent, and `dataDir: "."` deleted the working
tree. These are not contrived; `dataDir` is a required-looking string in
`StartOptions`, and passing a wrong value is the ordinary mistake a recursive
delete should be designed to survive. The behavior was also in the published
`dist/`, so it was reachable by any consumer of the package on 0.2.x.

The session API already got this right: `openStow` tracks whether it created the
directory and removes only in that case (`session.ts`), and its tests assert a
caller-supplied directory is left alone. The hazard was specific to the older
`Stow.start()` surface, which is why it survived alongside a correct
implementation of the same idea.

Nothing in the 0.2 documentation said a reset was restricted in any way. The
word "slate" implies a blank page, not a recursive delete of a path the caller
named.

## Decision

**Stow may delete a data directory only when it has evidence it created that
directory, and never a path that is the developer's own.**

The filesystem store writes a `.stow-owner` marker into the data directory when
it finishes initializing it, which is the point at which stow owns the
directory's layout. The rule has two independent layers, and both must pass:

1. **Ownership.** The directory must carry the marker, or must not exist at all.
   A directory that does not exist is already in the requested state, so
   resetting before the first run is not an error.
2. **Protected paths.** Even with a marker, the resolved path must not be the
   filesystem root, the user's home directory, the working directory, or an
   ancestor of any of them.

The second layer exists because the first is not sufficient on its own. A marker
is a file: it can be copied, committed by accident, or left behind by a stow
process that once used a path a developer has since adopted. The backstop costs
nothing and still holds when the marker is not trustworthy.

The protected-path check compares **resolved** paths and tests for
*ancestor-or-self*, not for equality. This is what closes the obvious bypass:
from `/home/dev/project`, the strings `..` and `../..` resolve to the home
directory and the filesystem root, but neither is literally either path, so an
equality check would wave both through. The check must not be so blunt that the
documented `dataDir: ".stow"` default stops working, which is why it is
ancestor-or-self rather than descendant.

A refusal is an error, not a silent no-op, and it names the reason. A developer
who expected a reset should not have to infer from a stale directory that
nothing happened.

The writer of the marker is Go (`internal/storage/fs/owner.go`) and the reader is
TypeScript (`packages/stow/src/ownership.ts`). Two copies of a name that a
delete decision depends on will drift, so `scripts/check-version.mjs` fails if
they differ, in the same way it already holds the session `GOGC` and the platform
package set in step.

## Naming

`cleanSlate` is retained as a deprecated alias for `resetOwnedData`. Both now
carry identical checks, so keeping the old name is harmless and avoids breaking
callers who want the behavior. The old name is the weaker of the two — it
describes an effect rather than a permission — so the new name is the one the
documentation and the types lead with.

The alias was kept rather than removed outright because the plan's own
compatibility rule is additive migration. That rule does not extend to
*unsafely* deleting a home directory, which is the case where a deprecation
period would be the wrong answer; the behavior change is intentional and is
recorded in the changelog.

## Consequences

- A caller who points `resetOwnedData` at a directory stow has never used now
  gets a `StowOwnershipError` explaining that stow did not create it. The
  legitimate workflow, resetting a stale `.stow` left by a previous run, is
  unaffected because the marker is present.
- A data directory created before this change has no marker, so the first reset
  after upgrading is refused. It is not deleted, so no data is lost; the
  operator removes it once by hand and stow recreates it with a marker.
- A marker write failure is logged, not fatal. Its absence only makes a later
  reset refuse, which is the safe direction to fail in, and refusing to start a
  server over a read-only data directory would be worse.
- `resetOwnedData` and `StowOwnershipError` are exported, so a caller managing
  its own data directories can check ownership without starting a server.

## Considered Options

- **Trust a name convention** such as the directory being called `.stow`.
  Rejected. It is a naming coincidence, not evidence of ownership, and it is
  trivial to satisfy by accident.
- **Only refuse protected paths, no marker.** Rejected. It protects a developer's
  home and working tree, which is the worst case, but not the ordinary case of a
  wrong `dataDir` pointing at some other project directory.
- **Only require the marker, no protected-path check.** Rejected for the reason in
  the decision; a marker can be present where ownership is not implied.
- **Deprecate the option for two releases before it starts checking.** Rejected.
  The window in which it deletes a home directory is exactly the window in which
  it matters. The check ships with the deprecation.
