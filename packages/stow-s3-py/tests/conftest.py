"""Shared fixtures for the Python test suite."""

from __future__ import annotations

import os
from pathlib import Path

import pytest


@pytest.fixture
def pid_file(tmp_path: Path) -> Path:
    """Where a spawned child records its server pid, for the orphan test."""
    path = tmp_path / "server.pid"
    os.environ["STOW_TEST_PID_FILE"] = str(path)
    try:
        yield path
    finally:
        os.environ.pop("STOW_TEST_PID_FILE", None)
