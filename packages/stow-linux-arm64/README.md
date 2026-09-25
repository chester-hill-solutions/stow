# @chs/stow-linux-arm64

Native `stow` server binary for linux arm64.

This package is an optional dependency of [`@chs/stow`](https://www.npmjs.com/package/@chs/stow)
and is installed automatically on matching platforms. It exists so that
`Stow.start()` works from a plain `npm install` with no `PATH` setup and no
separate binary installation.

It contains no JavaScript. Import it only indirectly, through
`resolveStowBinary()` from `@chs/stow`.

The binary is built by the repository release workflow from the same commit as
the main package, and the version is kept in step by `make check-version`.
