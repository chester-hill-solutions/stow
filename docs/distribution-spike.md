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

- **The PyPI distribution name is `stow-s3`, decided 2026-09-25.** `stow` is
  taken on PyPI by an unrelated artefact-management package (`stow` 1.4.1,
  "stow artefacts anywhere, with ease"), which the plan's warning anticipated.
  `stow-s3` was confirmed unclaimed at decision time.
- **The import package is `stow_s3`**, following the usual convention of
  normalizing a hyphenated distribution name. The plan's `from stow import
  session` sketch cannot be used: that import name belongs to the unrelated
  package and is not the name being published.
- **Supported Python versions are 3.10 and newer**, which is what the plan's own
  API sketch requires: it uses `int | None` unions and builtin generic
  `Iterator[...]` annotations, both of which are 3.10 syntax.
- **The binary ships inside a platform wheel of the one distribution, decided
  2026-09-25.** `pyproject.toml` force-includes the Go build output at
  `stow_s3/_bin/stow-s3`, and PyPI selects the matching wheel from its platform tag.
  npm optional dependencies have no PyPI equivalent, and a wheel keeps the binary
  in the same distribution and version as the Python code, so the existing
  version skew gate extends to it without adding separately versioned artifacts
  that must each track the Go binary. The alternatives rejected were
  per-platform distributions with marker-conditional dependencies, an
  install-time download, and a PEP 517 backend that builds Go during pip
  install; each trades an offline, toolchain-free install for something worse.
- `boto3` stays an optional extra. The session performs no S3 I/O and creates no
  bucket, so nothing in the package needs boto3; it is only needed by
  `Session.s3_client`, and a user with a different S3 library can build a client
  from the endpoint and credentials directly.
- **The clean-virtualenv experiment passes.** A wheel was built, installed into a
  fresh virtualenv with no `STOW_BIN` and no `stow` on `PATH`, and a session was
  opened and used for a real put/get/list round trip. The binary resolved from
  `stow_s3/_bin/stow-s3` with source `platform-wheel`, the reported limits were the
  session defaults the server was actually enforcing, and the credentials
  appeared on neither the readiness channel's sibling stream nor the logs.

Two Python-specific findings from that work, both of which would have been
expensive to rediscover:

- **The readiness descriptor cannot be pinned to fd 3 the way Node pins it.**
  `pass_fds` implies `close_fds`, and CPython closes inherited descriptors above
  2 *after* `preexec_fn` runs, so a `dup2` onto fd 3 is undone before `exec` and
  the child sees `EBADF`. Because the server already takes a descriptor number,
  the fix is to pass whatever `os.pipe()` returned. That removes the need for
  `preexec_fn` entirely, along with the thread-safety hazard it carries.
- **A live child's output cannot be read from a pipe.** `read(n)` blocks until `n`
  bytes arrive or the child exits, which for a running server is a deadlock, not
  a slow read. Output goes to temporary files instead, so diagnostics are
  readable while the server runs and survive it exiting.

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
  searched for in this order: the STOW_BIN environment variable, bin/stow-s3
  relative to a monorepo checkout, then stow on PATH. Install the platform
  binary package for this platform, point STOW_BIN at an existing binary, or
  use the EmbeddedStow and @chs/stow-s3/browser profiles, which need no server
  binary.
```

## 7. Next steps

1. ~~Add `@chs/stow-s3-<platform>` optional packages and teach `resolveStowBinary` to
   prefer a bundled platform binary over `PATH`.~~ Done.
2. ~~Run the clean-virtualenv Python experiment for the `stow-s3`
   distribution.~~ Done, and passing.
3. Record the benchmark baseline, which remains the last open phase 0 item.

Still open, and not part of what has landed:

- The Python package is built and tested, but not published. Publishing needs a
  PyPI account and a release pipeline that cross-compiles the binary per platform
  before the wheel is built, because `pyproject.toml` force-includes the Go
  build output.
- No platform other than linux x64 has been measured, and the Python client's
  own startup cost is unmeasured. The 15.4 ms ready p50 in the benchmark is the
  server's, measured from the TypeScript client.
