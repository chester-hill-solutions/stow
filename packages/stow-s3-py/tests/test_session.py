"""Session tests that need the native binary.

These are skipped when no binary can be resolved, so the protocol and resolution
tests still run on a machine that has not built one.
"""

from __future__ import annotations

import os
import signal
import subprocess
import sys
import time
from pathlib import Path

import pytest

from stow_s3 import (
    DEFAULT_SESSION_MAX_BYTES,
    DEFAULT_SESSION_MAX_OBJECTS,
    READY_PROTOCOL_VERSION,
    SESSION_GOGC,
    build_child_env,
    open_session,
    stow_binary_available,
    with_session,
)

needs_binary = pytest.mark.skipif(
    not stow_binary_available(), reason="no stow binary is available"
)


def test_session_child_env_tightens_the_collector(monkeypatch: pytest.MonkeyPatch) -> None:
    """A session runs its own server with a tighter Go collector target."""
    monkeypatch.delenv("GOGC", raising=False)
    assert build_child_env()["GOGC"] == SESSION_GOGC
    assert SESSION_GOGC != "100", "a session that matches the Go default is not tuning anything"


def test_session_child_env_keeps_a_caller_chosen_collector(monkeypatch: pytest.MonkeyPatch) -> None:
    """An explicit GOGC in the environment outranks the session default."""
    monkeypatch.setenv("GOGC", "400")
    assert build_child_env()["GOGC"] == "400"


def test_session_child_env_still_strips_cloud_configuration(monkeypatch: pytest.MonkeyPatch) -> None:
    """Stripping ambient configuration must not take the collector target with it."""
    monkeypatch.delenv("GOGC", raising=False)
    monkeypatch.setenv("S3_ENDPOINT_URL", "https://elsewhere.example")
    monkeypatch.setenv("AWS_PROFILE", "production")
    env = build_child_env()
    assert "S3_ENDPOINT_URL" not in env
    assert "AWS_PROFILE" not in env
    assert env["GOGC"] == SESSION_GOGC

boto3 = pytest.importorskip("boto3")


def put_and_get(session: object) -> str:
    s3 = session.s3_client()  # type: ignore[attr-defined]
    bucket = session.new_bucket_name()  # type: ignore[attr-defined]
    s3.create_bucket(Bucket=bucket)
    s3.put_object(Bucket=bucket, Key="input.json", Body=b'{"task":"summarize"}')
    body = s3.get_object(Bucket=bucket, Key="input.json")["Body"].read()
    s3.close()
    return body.decode()


@needs_binary
def test_round_trip_inside_a_context_manager() -> None:
    with with_session() as session:
        assert put_and_get(session) == '{"task":"summarize"}'


@needs_binary
def test_reports_the_endpoint_and_credentials() -> None:
    with with_session() as session:
        assert session.endpoint.startswith("http://127.0.0.1:")
        assert session.access_key_id
        assert session.secret_access_key
        assert session.region
        assert session.protocol_version == READY_PROTOCOL_VERSION
        assert session.binary_version


@needs_binary
def test_reports_the_limits_the_server_is_actually_enforcing() -> None:
    # A session must not report a limit it assumed. The numbers come from the
    # ready message, so what a caller is told is what the server enforces.
    with with_session() as session:
        assert session.capabilities.max_bytes == DEFAULT_SESSION_MAX_BYTES
        assert session.capabilities.max_objects == DEFAULT_SESSION_MAX_OBJECTS
        assert session.max_bytes == DEFAULT_SESSION_MAX_BYTES


@needs_binary
def test_defaults_match_the_measured_decision() -> None:
    # 16 MiB and 1000 objects came from measurement, not taste. If these change,
    # the benchmark document should change with them.
    assert DEFAULT_SESSION_MAX_BYTES == 16 * 1024 * 1024
    assert DEFAULT_SESSION_MAX_OBJECTS == 1000


@needs_binary
def test_each_session_gets_a_distinct_endpoint() -> None:
    with with_session() as first, with_session() as second:
        assert first.endpoint != second.endpoint


@needs_binary
def test_credentials_never_reach_stdout() -> None:
    # The legacy STOW_READY line carries credentials on stdout. Asking for the
    # versioned channel must suppress it entirely, not print both. Stdout still
    # carries the human-readable banner, so the assertion is about the
    # credentials rather than about stdout being empty.
    with with_session() as session:
        stdout = session.stdout_text
        assert "stow listening on" in stdout, "the banner is expected on stdout"
        assert "STOW_READY" not in stdout
        assert session.access_key_id not in stdout
        assert session.secret_access_key not in stdout


@needs_binary
def test_closing_stops_the_server() -> None:
    session = open_session()
    pid = session._process.pid
    assert session._process.poll() is None
    session.close()
    assert session._process.poll() is not None
    assert not _pid_alive(pid)


@needs_binary
def test_closing_twice_is_harmless() -> None:
    session = open_session()
    session.close()
    session.close()


@needs_binary
def test_closing_removes_the_data_directory() -> None:
    # Asserting on the session's own directory rather than a snapshot of the
    # temporary directory, which concurrent suites make unreliable.
    session = open_session()
    data_dir = session.data_dir
    assert data_dir.is_dir()
    session.close()
    assert not data_dir.exists()


@needs_binary
def test_the_context_manager_cleans_up_after_an_exception() -> None:
    captured: dict[str, object] = {}
    with pytest.raises(RuntimeError, match="boom"):
        with with_session() as session:
            captured["data_dir"] = session.data_dir
            captured["pid"] = session._process.pid
            raise RuntimeError("boom")
    assert not Path(captured["data_dir"]).exists()  # type: ignore[arg-type]
    assert not _pid_alive(int(captured["pid"]))  # type: ignore[arg-type]


@needs_binary
def test_a_session_keeps_each_callers_own_configuration() -> None:
    with with_session(max_bytes=1024, max_objects=7) as session:
        assert session.capabilities.max_bytes == 1024
        assert session.capabilities.max_objects == 7


@needs_binary
def test_a_negative_limit_is_rejected_before_anything_starts() -> None:
    with pytest.raises(ValueError, match="must not be negative"):
        open_session(max_bytes=-1)
    with pytest.raises(ValueError, match="must not be negative"):
        open_session(max_objects=-1)


@needs_binary
def test_the_server_does_not_inherit_ambient_cloud_configuration(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    # If the server inherited these it would try to reach a real bucket, and the
    # session would be neither local nor isolated.
    monkeypatch.setenv("AWS_ACCESS_KEY_ID", "ambient")
    monkeypatch.setenv("AWS_SECRET_ACCESS_KEY", "ambient")
    monkeypatch.setenv("S3_ENDPOINT", "https://s3.example.invalid")
    monkeypatch.setenv("STOW_POLICY", "mirrorWrites")
    with with_session() as session:
        assert session.mode == "local"
        assert session.capabilities.upstream is False
        assert "/health" not in session.endpoint


def _pid_alive(pid: int) -> bool:
    try:
        os.kill(pid, 0)
    except (ProcessLookupError, PermissionError):
        return False
    return True


@pytest.mark.skipif(
    not stow_binary_available(), reason="no stow binary is available"
)
def test_fifty_sessions_leave_nothing_behind() -> None:
    # The lifecycle gate. Each session asserts its own directory and its own
    # child, so a concurrent suite running alongside cannot make this flaky.
    pids: list[int] = []
    directories: list[Path] = []
    for _ in range(50):
        session = open_session()
        s3 = session.s3_client()
        bucket = session.new_bucket_name()
        s3.create_bucket(Bucket=bucket)
        s3.put_object(Bucket=bucket, Key="k", Body=b"v")
        s3.close()
        pids.append(session._process.pid)
        directories.append(session.data_dir)
        session.close()

    for directory in directories:
        assert not directory.exists(), f"{directory} survived close"
    for pid in pids:
        assert not _pid_alive(pid), f"server {pid} survived close"
    assert len(set(directories)) == len(directories), "data directories were reused"


@pytest.mark.skipif(
    not stow_binary_available(), reason="no stow binary is available"
)
def test_a_killed_client_leaves_no_orphan_server(pid_file: Path) -> None:
    """The abrupt-death case a graceful close cannot reach.

    A child Python process opens a session and is then SIGKILLed, so no close
    handler runs. The server watches the parent, so it must exit on its own.
    """
    script = f"""
import os, sys
sys.path.insert(0, {str(Path(__file__).resolve().parents[1] / "src")!r})
from stow_s3 import open_session
session = open_session()
open({str(pid_file)!r}, "w").write(str(session._process.pid))
import time
time.sleep(300)
"""
    child = subprocess.Popen(
        [sys.executable, "-c", script],
        stdout=subprocess.DEVNULL,
        stderr=subprocess.PIPE,
        env=build_child_env(),
    )
    try:
        deadline = time.monotonic() + 30
        server_pid = None
        while time.monotonic() < deadline:
            if pid_file.exists():
                server_pid = int(pid_file.read_text().strip())
                break
            time.sleep(0.05)
        assert server_pid is not None, "the child never reported a server pid"
        assert _pid_alive(server_pid), "the server was not running before the kill"

        child.kill()
        child.wait(timeout=15)

        gone_deadline = time.monotonic() + 20
        while time.monotonic() < gone_deadline:
            if not _pid_alive(server_pid):
                break
            time.sleep(0.1)
        assert not _pid_alive(server_pid), (
            f"server {server_pid} outlived the client that was killed"
        )
    finally:
        if child.poll() is None:
            child.kill()
            child.wait(timeout=10)
        pid_file.unlink(missing_ok=True)
