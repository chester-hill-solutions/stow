#!/usr/bin/env python3
"""Build the stow-s3 wheel for a specific target platform.

The wheel force-includes the Go binary, so it is only valid for the platform it
was built against. Hatchling has no way to say that: with no compiled extension
it tags the result ``py3-none-any``, which tells pip the wheel is portable. It
is not. A ``py3-none-any`` wheel carrying a Linux binary would install happily on
an Apple Silicon Mac and then fail with an exec format error at run time, which
is a far worse failure than being unable to install at all.

The release matrix cross-compiles all four binaries from ubuntu, so the build
machine is never the target machine and the inferred tag is always wrong. This
builds the wheel and then retags it for the platform it was actually built for.

The archive is rewritten rather than renamed, because the tag is recorded in two
places: the filename and the ``Tag:`` field in ``.dist-info/WHEEL``. Renaming
alone leaves the metadata claiming portability. RECORD is regenerated at the same
time, since changing WHEEL invalidates its recorded hash.
"""

from __future__ import annotations

import argparse
import base64
import csv
import hashlib
import io
import shutil
import stat
import subprocess
import sys
import tempfile
import zipfile
from pathlib import Path

# The binary ships inside the package and is the reason the wheel exists.
BINARY_MEMBER = "stow_s3/_bin/stow-s3"

# Wheel tags per release matrix entry. These are the oldest platform each build
# actually runs on, so a wheel is never offered to a machine it cannot run on.
PLATFORM_TAGS = {
    "linux-amd64": "manylinux_2_17_x86_64",
    "linux-arm64": "manylinux_2_17_aarch64",
    "darwin-amd64": "macosx_11_0_x86_64",
    "darwin-arm64": "macosx_11_0_arm64",
}


def record_hash(data: bytes) -> str:
    digest = hashlib.sha256(data).digest()
    return "sha256=" + base64.urlsafe_b64encode(digest).rstrip(b"=").decode()


def build_wheel(project_dir: Path, out_dir: Path) -> Path:
    subprocess.run(
        [sys.executable, "-m", "build", "--wheel", "--outdir", str(out_dir), "."],
        cwd=project_dir,
        check=True,
    )
    wheels = list(out_dir.glob("*.whl"))
    if len(wheels) != 1:
        raise SystemExit(f"expected exactly one wheel, found {len(wheels)}")
    return wheels[0]


def retag(wheel: Path, platform_tag: str) -> Path:
    """Rewrite the wheel so its filename and metadata name the same platform."""
    with zipfile.ZipFile(wheel) as source:
        members = {name: source.read(name) for name in source.namelist()}

    if BINARY_MEMBER not in members:
        raise SystemExit(
            f"wheel does not carry {BINARY_MEMBER}; a wheel without the binary "
            "cannot start a server, so publishing it would ship something that "
            "only appears to work"
        )

    wheel_metadata = next((n for n in members if n.endswith(".dist-info/WHEEL")), None)
    if wheel_metadata is None:
        raise SystemExit("wheel has no .dist-info/WHEEL")
    record_name = wheel_metadata.replace("/WHEEL", "/RECORD")
    if record_name not in members:
        raise SystemExit("wheel has no .dist-info/RECORD")

    new_tag = f"py3-none-{platform_tag}"
    members[wheel_metadata] = _replace_tag(members[wheel_metadata], new_tag)
    members[record_name] = _regenerate_record(members, record_name)

    # The tag lives in the filename as the last three dash-separated fields.
    prefix = wheel.name.split("-py3-")[0]
    target = wheel.with_name(f"{prefix}-{new_tag}.whl")

    with zipfile.ZipFile(target, "w", zipfile.ZIP_DEFLATED) as out:
        for name, data in members.items():
            if name == BINARY_MEMBER:
                out.writestr(_executable_member(name), data)
            elif name == record_name:
                # RECORD conventionally sorts first and is stored uncompressed by
                # most builders; neither is required, but matching them keeps the
                # archive diffable against one built the usual way.
                out.writestr(_stored_member(name), data)
            else:
                out.writestr(name, data)
    return target


def _executable_member(name: str) -> zipfile.ZipInfo:
    """A zip entry carrying the executable bit.

    A wheel records POSIX permissions in the entry's external attributes, and pip
    restores them on install. The build backend does not carry the mode across
    for a force-included file, so the binary arrives non-executable and every
    session fails to start on a permission error that names neither the binary
    nor the packaging step that dropped the bit.

    The file-type bits matter as much as the permission bits. pip only chmods a
    member when ``zip_item_is_executable`` is true, and that requires
    ``stat.S_ISREG(mode)`` as well as an execute bit. A mode of ``0o755`` has
    permission bits but no file-type bits, so S_ISREG is false and pip leaves the
    file non-executable even though the mode looks right in the archive. Both
    halves are set here.

    Same class of bug as the committed wasm_exec.js losing its mode, and just as
    invisible until the artifact is actually installed somewhere.
    """
    info = zipfile.ZipInfo(name)
    info.external_attr = (stat.S_IFREG | 0o755) << 16
    info.compress_type = zipfile.ZIP_DEFLATED
    return info


def _stored_member(name: str) -> zipfile.ZipInfo:
    info = zipfile.ZipInfo(name)
    info.external_attr = (stat.S_IFREG | 0o644) << 16
    info.compress_type = zipfile.ZIP_STORED
    return info


def _replace_tag(metadata: bytes, new_tag: str) -> bytes:
    lines = []
    replaced = False
    for line in metadata.decode().splitlines():
        if line.startswith("Tag:"):
            lines.append(f"Tag: {new_tag}")
            replaced = True
        else:
            lines.append(line)
    if not replaced:
        raise SystemExit("wheel metadata has no Tag: field to replace")
    return ("\n".join(lines) + "\n").encode()


def _regenerate_record(members: dict[str, bytes], record_name: str) -> bytes:
    buffer = io.StringIO()
    writer = csv.writer(buffer, lineterminator="\n")
    for name in sorted(members):
        if name == record_name:
            # RECORD lists itself with no hash and no size.
            writer.writerow([name, "", ""])
        else:
            data = members[name]
            writer.writerow([name, record_hash(data), len(data)])
    return buffer.getvalue().encode()


def required_binary(project: Path) -> Path:
    """Resolve the binary the wheel force-includes, from the project config.

    Read from pyproject.toml rather than hardcoded, so that changing the
    force-include path cannot leave this check guarding a file the wheel no
    longer uses.
    """
    try:
        import tomllib
    except ModuleNotFoundError:  # pragma: no cover - Python 3.10 fallback
        raise SystemExit("building this wheel needs Python 3.11+ for tomllib")

    config = tomllib.loads((project / "pyproject.toml").read_text())
    force_include = (
        config.get("tool", {})
        .get("hatch", {})
        .get("build", {})
        .get("targets", {})
        .get("wheel", {})
        .get("force-include", {})
    )
    sources = [key for key in force_include if key.endswith("/stow-s3")]
    if len(sources) != 1:
        raise SystemExit(
            f"expected exactly one force-included stow-s3 binary, found {sources}"
        )
    return (project / sources[0]).resolve()


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--target",
        required=True,
        choices=sorted(PLATFORM_TAGS),
        help="release matrix entry this wheel is built for",
    )
    parser.add_argument(
        "--project",
        default="packages/stow-s3-py",
        help="path to the Python project",
    )
    parser.add_argument("--out", required=True, help="directory to write the wheel to")
    args = parser.parse_args()

    project = Path(args.project).resolve()
    if not (project / "pyproject.toml").exists():
        raise SystemExit(f"no pyproject.toml in {project}")

    binary = required_binary(project)
    if not binary.exists():
        raise SystemExit(
            f"{binary} is missing; the wheel force-includes it, so build the "
            "binary for this target before building the wheel"
        )

    out_dir = Path(args.out).resolve()
    out_dir.mkdir(parents=True, exist_ok=True)
    # A stale wheel from a previous run would otherwise be picked up as "the one
    # wheel" and published under the wrong tag.
    for stale in out_dir.glob("*.whl"):
        stale.unlink()

    wheel = build_wheel(project, out_dir)
    tagged = retag(wheel, PLATFORM_TAGS[args.target])
    wheel.unlink()

    with zipfile.ZipFile(tagged) as archive:
        if BINARY_MEMBER not in archive.namelist():
            raise SystemExit("retagged wheel lost its binary")
    print(f"{tagged.name} ({tagged.stat().st_size / (1024 * 1024):.1f} MB)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
