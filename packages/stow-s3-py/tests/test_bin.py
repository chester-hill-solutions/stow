"""Tests for binary resolution and environment isolation.

Resolution order matters: the packaged binary wins for a wheel install, STOW_BIN
overrides everything for development, and PATH is the last resort. Getting the
order wrong sends a user to the wrong place when they are trying to debug an
install.
"""

from __future__ import annotations

from pathlib import Path

import pytest

from stow_s3 import (
    BinarySource,
    StowBinaryNotFoundError,
    build_child_env,
    describe_source,
    require_stow_binary,
    resolve_stow_binary_detailed,
    stow_binary_available,
)


def test_reports_which_source_resolved_the_binary() -> None:
    resolved = resolve_stow_binary_detailed()
    assert resolved.path
    assert isinstance(resolved.source, BinarySource)
    # A source with no human explanation would leave a user unable to act on it.
    assert describe_source(resolved.source)


def test_environment_variable_wins_when_it_points_somewhere_real(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    binary = tmp_path / "stow"
    binary.write_text("#!/bin/sh\nexit 0\n")
    binary.chmod(0o755)
    monkeypatch.setenv("STOW_BIN", str(binary))
    resolved = resolve_stow_binary_detailed()
    assert resolved.path == str(binary)
    assert resolved.source is BinarySource.ENVIRONMENT
    assert stow_binary_available()


def test_a_directory_is_not_mistaken_for_a_binary(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    # exists() alone would accept a directory and defer the failure to spawn.
    monkeypatch.setenv("STOW_BIN", str(tmp_path))
    assert not stow_binary_available()
    with pytest.raises(StowBinaryNotFoundError) as caught:
        require_stow_binary()
    assert caught.value.code == "binary_not_found"


def test_a_non_executable_file_is_not_mistaken_for_a_binary(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    binary = tmp_path / "stow"
    binary.write_text("not executable")
    binary.chmod(0o644)
    monkeypatch.setenv("STOW_BIN", str(binary))
    assert not stow_binary_available()


def test_a_missing_binary_explains_the_resolution_order(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    monkeypatch.setenv("STOW_BIN", str(tmp_path / "absent"))
    with pytest.raises(StowBinaryNotFoundError) as caught:
        require_stow_binary()
    message = str(caught.value)
    # The whole point of this error is that it says what to do next.
    assert "STOW_BIN" in message
    assert "PATH" in message
    assert "stow-s3" in message


def test_a_session_does_not_inherit_cloud_configuration(
    monkeypatch: pytest.MonkeyPatch
) -> None:
    # A stray credential in the caller's shell would otherwise turn a local
    # session into a run-through one against a real bucket.
    monkeypatch.setenv("AWS_SECRET_ACCESS_KEY", "must-not-reach-child")
    monkeypatch.setenv("AWS_ACCESS_KEY_ID", "must-not-reach-child")
    monkeypatch.setenv("S3_ENDPOINT", "https://s3.example.invalid")
    monkeypatch.setenv("STOW_POLICY", "mirrorWrites")
    monkeypatch.setenv("PATH", "/usr/bin")
    monkeypatch.setenv("HOME", "/home/someone")

    env = build_child_env()
    assert env["PATH"] == "/usr/bin"
    assert env["HOME"] == "/home/someone"
    for key in env:
        assert not key.startswith(("STOW_", "S3_", "AWS_")), f"{key} leaked into the child"


def test_session_values_are_added_after_the_prefixes_are_stripped(
    monkeypatch: pytest.MonkeyPatch
) -> None:
    # Stripping must not remove values the session itself sets, or a caller
    # could not pass an explicit key through.
    monkeypatch.setenv("STOW_POLICY", "mirrorWrites")
    env = build_child_env({"STOW_LOCAL_ACCESS_KEY_ID": "explicit"})
    assert env["STOW_LOCAL_ACCESS_KEY_ID"] == "explicit"
    assert "STOW_POLICY" not in env
