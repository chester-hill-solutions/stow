"""Locating the native stow server binary.

The resolution order and the shape of the result mirror the TypeScript client so
a user moving between the two languages sees the same behaviour and the same
diagnostic. The source matters as much as the path: "nothing is installed" and
"STOW_BIN points at a binary from an older release" are different problems with
different fixes, and a bare path cannot tell them apart.
"""

from __future__ import annotations

import os
import shutil
from dataclasses import dataclass
from enum import Enum
from pathlib import Path

#: Name of the binary bundled in a platform wheel, relative to the package.
BUNDLED_SUBPATH = Path("_bin") / "stow"

#: How far above the package to look for a monorepo checkout.
MONOREPO_DEPTH = 8


class BinarySource(str, Enum):
    """Which rule produced the resolved path."""

    PLATFORM_WHEEL = "platform-wheel"
    ENVIRONMENT = "environment"
    MONOREPO = "monorepo"
    PATH = "path"


@dataclass(frozen=True)
class ResolvedBinary:
    path: str
    source: BinarySource


class StowBinaryNotFoundError(Exception):
    """No runnable server binary was found.

    The underlying spawn failure for a missing binary is a bare ``ENOENT`` on the
    literal string ``stow``, which tells someone who installed from PyPI nothing
    about why their session did not start.
    """

    code = "binary_not_found"

    def __init__(self, resolved: str) -> None:
        super().__init__(
            f"The stow server binary was not found (resolved to {resolved!r}). "
            "It is searched for in this order: the platform wheel that ships it "
            "inside this package, the STOW_BIN environment variable, bin/stow "
            "relative to a monorepo checkout, then stow on PATH. Install stow-s3 "
            "for this platform, point STOW_BIN at an existing binary, or set "
            "STOW_BIN to a downloaded release."
        )
        self.resolved = resolved
        self.name = "StowBinaryNotFoundError"


def _is_executable(path: Path) -> bool:
    # A path that exists but is not executable is worse than one that is absent,
    # because a directory can satisfy exists() and only fail at spawn.
    return path.is_file() and os.access(path, os.X_OK)


def _bundled_binary() -> Path | None:
    """The binary shipped inside a platform wheel, if this is one."""
    candidate = Path(__file__).resolve().parent / BUNDLED_SUBPATH
    return candidate if _is_executable(candidate) else None


def _monorepo_binary() -> Path | None:
    directory = Path(__file__).resolve().parent
    for _ in range(MONOREPO_DEPTH):
        if (directory / "go.mod").is_file():
            candidate = directory / "bin" / "stow"
            if _is_executable(candidate):
                return candidate
        parent = directory.parent
        if parent == directory:
            break
        directory = parent
    return None


def resolve_stow_binary_detailed() -> ResolvedBinary:
    """Resolve the binary path and report which rule produced it."""
    bundled = _bundled_binary()
    if bundled is not None:
        return ResolvedBinary(str(bundled), BinarySource.PLATFORM_WHEEL)

    from_env = (os.environ.get("STOW_BIN") or "").strip()
    if from_env:
        return ResolvedBinary(from_env, BinarySource.ENVIRONMENT)

    in_repo = _monorepo_binary()
    if in_repo is not None:
        return ResolvedBinary(str(in_repo), BinarySource.MONOREPO)

    return ResolvedBinary("stow", BinarySource.PATH)


def resolve_stow_binary() -> str:
    return resolve_stow_binary_detailed().path


def stow_binary_available() -> bool:
    """Whether a resolved path is actually runnable."""
    resolved = resolve_stow_binary_detailed()
    if resolved.source is BinarySource.PATH:
        return shutil.which("stow") is not None
    return _is_executable(Path(resolved.path))


def require_stow_binary() -> ResolvedBinary:
    """Resolve the binary, raising an actionable error when it is not runnable."""
    resolved = resolve_stow_binary_detailed()
    if not stow_binary_available():
        raise StowBinaryNotFoundError(resolved.path)
    return resolved


def describe_source(source: BinarySource) -> str:
    """A human explanation of a resolution source."""
    return {
        BinarySource.PLATFORM_WHEEL: "the platform wheel that ships it in this package",
        BinarySource.ENVIRONMENT: "the STOW_BIN environment variable",
        BinarySource.MONOREPO: "bin/stow in the monorepo checkout",
        BinarySource.PATH: "stow on PATH",
    }[source]
