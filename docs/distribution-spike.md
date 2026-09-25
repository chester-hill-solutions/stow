# Distribution spike: can a clean install start a Stow session?

**Status:** complete
**Date:** 2026-09-25
**Answers:** the npm distribution question in `docs/agent-dx-plan.md` phase 0, plus
the first-pass Python feasibility check.
**Method:** clean-room experiment, not inspection. A packed tarball was installed
into an empty directory outside the monorepo, with no `stow` on `PATH`, and a
real session was attempted.

## 1. Result

**No. A clean `npm install` cannot start a session, and the npm package ships no
native binary at all.** The session path itself is fine: given a binary, the same
clean-room install starts and stops a session successfully. The gap is
distribution, not session logic.

## 2. Measurements

| Measurement | Value |
|---|---|
| Stripped `stow` binary (linux/amd64) | 11.0 MB |
| Same binary, gzip -9 | 4.0 MB |
| Same binary, xz -9 | 3.0 MB |
| Current npm tarball | 1.2 MB (89 files) |
| Current installed package | 4.4 MB |
| Projected installed size with one platform binary | ~8.4 MB compressed-equivalent |
| Clean-room `npm install` time | 1.9 s |
| Platforms already built by the release pipeline | linux/amd64, linux/arm64, darwin/amd64, darwin/arm64 |
| `npm publish` provenance | already enabled |
| Native artifact checksums | already published as `SHA256SUMS.txt` |

The package is dominated by a 4.0 MB WASM artifact that a child-process session
does not use. A session user who never touches `EmbeddedStow` would carry it
today and would need the ~4 MB binary as well.

## 3. Observed failures

Before the fix in this spike:

```text
code: ENOENT
name: Error
message: spawn stow ENOENT
```

`stowBinaryAvailable()` already existed in the package and was **exported but
never called**, so the missing-binary case was reported by `spawn` as a bare
`ENOENT` against the literal string `stow`. That tells a user who installed from
npm nothing about why their session did not start.

## 4. Conclusion and recommendation

**Ship the binary as platform-scoped npm optional packages.** The decision is
much easier than the plan assumed, because the hard parts already exist:

- the release pipeline already builds all four candidate platforms;
- npm publishing already uses `--provenance`, and npm's own integrity hashes
  cover each optional package;
- `SHA256SUMS.txt` already exists for the native tarballs.

The remaining work is packaging and resolution, not a new build or signing story.
A companion downloader was the alternative, and it is strictly worse for the
common path: it adds a network dependency and a second trust decision at runtime,
and it cannot work offline or in a locked-down CI environment.

Cost to accept: a user's install grows by roughly 4 MB for their platform, and
every platform becomes a published artifact that must be kept in step with the
binary version.

**Reconsider** if install size becomes a measured problem. In that case the
better move is to stop shipping the 4 MB WASM artifact in the default package and
move it behind its own subpath package, which is a separate decision.

## 5. Python: first-pass findings

- **`stow` is taken on PyPI.** It is an unrelated artefact-management package
  (`stow` 1.4.1, "stow artefacts anywhere, with ease"). The plan's warning not to
  assume the name is available was correct. `chs-stow` and `stow-s3` are both
  free.
- The same binary distribution question applies. Platform wheels are viable
  because the release pipeline already cross-compiles, but each platform wheel
  becomes a separately versioned artifact that must track the Go binary.
- `boto3` must stay an optional extra, as the plan already requires.

Not yet done: a clean-virtualenv install experiment, and a decision on the
distribution name. Both are blocked on nothing and can proceed once the name is
chosen.

## 6. What changed as a result of this spike

`Stow.start()` now checks for the binary before spawning and raises
`StowBinaryNotFoundError` with `code: "binary_not_found"`, naming the resolution
order and pointing at the profiles that need no server binary. This is correct
regardless of which distribution model is chosen, because an unsupported platform
will still hit the missing-binary path.

Verified in a clean room after the fix:

```text
code: binary_not_found
name: StowBinaryNotFoundError
message: The stow server binary was not found (resolved to "stow"). It is
  searched for in this order: the STOW_BIN environment variable, bin/stow
  relative to a monorepo checkout, then stow on PATH. Install the platform
  binary package for this platform, point STOW_BIN at an existing binary, or
  use the EmbeddedStow and @chs/stow/browser profiles, which need no server
  binary.
```

## 7. Next steps

1. Choose the PyPI distribution name (`chs-stow` recommended).
2. Add `@chs/stow-<platform>` optional packages and teach `resolveStowBinary` to
   prefer a bundled platform binary over `PATH`.
3. Run the clean-virtualenv Python experiment.
4. Record the benchmark baseline, which remains the last open phase 0 item.
