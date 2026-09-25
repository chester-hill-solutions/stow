"""A disposable S3 session backed by the native stow server.

The session owns the child process. Closing it stops the server and removes its
data directory, and the server watches the parent so that a client which is
killed rather than closed does not leave an orphan behind.

A session is deliberately narrow: it holds the endpoint and credentials and owns
the process, but performs no S3 I/O of its own. That is what lets an async
session be added later without changing anything here.
"""

from __future__ import annotations

import contextlib
import os
import secrets
import shutil
import subprocess
import tempfile
import threading
import time
from collections.abc import Iterator
from dataclasses import dataclass, field
from pathlib import Path
from typing import IO, Any

from .bin import require_stow_binary
from .ready import StowProtocolError, StowReady, parse_ready_message

#: Defaults derived from measurement, not chosen by feel. See
#: docs/benchmarks/session-baseline.md.
DEFAULT_SESSION_MAX_BYTES = 16 * 1024 * 1024
DEFAULT_SESSION_MAX_OBJECTS = 1000

#: How long to wait for the server to report readiness.
STARTUP_TIMEOUT_SECONDS = 15.0

#: Grace period between SIGTERM and SIGKILL when stopping the child. The Go
#: server allows itself 10s of shutdown budget, so this is longer than that.
STOP_GRACE_SECONDS = 10.0
STOP_WAIT_SECONDS = 15.0

#: Only the diagnostic tail is kept. A server that misbehaves can print a great
#: deal, and an unbounded buffer in a long test run is a leak.
MAX_DIAGNOSTIC_BYTES = 64 * 1024

#: Environment prefixes a session must not inherit. A stray credential in the
#: caller's shell would otherwise turn a local session into a run-through one.
_ISOLATED_ENV_PREFIXES = ("STOW_", "S3_", "AWS_")

#: The Go collector target a session's own server runs with. A session is
#: short-lived and one of many on the machine, so its peak memory is what matters
#: and its throughput rarely is: at GOGC=50 a session's peak RSS per MiB of
#: payload falls from 4.19 to 3.27 for roughly 6% more put latency. The
#: TypeScript client must use the same value; check-version.mjs fails if the two
#: drift.
SESSION_GOGC = "50"


def build_child_env(extra: dict[str, str] | None = None) -> dict[str, str]:
    """The child environment, with ambient cloud configuration removed."""
    env = {key: value for key, value in os.environ.items() if not key.startswith(_ISOLATED_ENV_PREFIXES)}
    # A caller who set GOGC themselves keeps their value: an explicit choice in
    # the environment outranks a default. GOGC does not match the prefixes above,
    # so it survives the strip and can be honoured.
    env.setdefault("GOGC", SESSION_GOGC)
    if extra:
        env.update(extra)
    return env


def _read_tail(handle: IO[bytes] | None, limit: int = MAX_DIAGNOSTIC_BYTES) -> str:
    """Read the tail of a log the server is still writing to.

    The child's output goes to temporary files rather than pipes precisely so
    this can be called while the server is still running. Reading a pipe from a
    live child blocks until the buffer fills or the child exits, which is a
    deadlock rather than a slow read.
    """
    if handle is None:
        return ""
    try:
        handle.seek(0, os.SEEK_END)
        size = handle.tell()
        handle.seek(max(0, size - limit))
        return handle.read().decode(errors="replace")
    except (OSError, ValueError):
        return ""


@dataclass(frozen=True)
class Session:
    """A running stow server and the parameters needed to reach it.

    The session performs no S3 I/O. It reports what the server is, hands back a
    client, and owns the process. Keeping I/O out is what lets an async session
    be added later without changing this type, and it is what keeps boto3 a truly
    optional extra rather than something every session needs.
    """

    endpoint: str
    access_key_id: str
    secret_access_key: str
    region: str
    data_dir: Path
    max_bytes: int
    max_objects: int
    mode: str
    backend: str
    binary_version: str
    protocol_version: int
    capabilities: Any
    _process: subprocess.Popen[bytes] = field(repr=False)
    _stdout_log: IO[bytes] | None = field(default=None, repr=False)
    _stderr_log: IO[bytes] | None = field(default=None, repr=False)
    _closed: bool = field(default=False, repr=False)

    @property
    def stdout_text(self) -> str:
        """Whatever the server wrote to stdout so far.

        A session asks for the versioned readiness channel, so this must never
        contain the credentials that the legacy STOW_READY line would have put
        there. It is exposed so a test can assert that.
        """
        return _read_tail(self._stdout_log)

    @property
    def stderr_text(self) -> str:
        return _read_tail(self._stderr_log)

    def new_bucket_name(self) -> str:
        """A bucket name that is very unlikely to collide with another session.

        Creating the bucket is left to the caller, so this is a name and nothing
        more. It is a convenience, not a claim that the bucket exists.
        """
        return f"stow-session-{secrets.token_hex(8)}"

    def s3_client(self, **kwargs: Any) -> Any:
        """Build a boto3 S3 client for this session.

        boto3 is an optional extra, so the import is deferred and the failure is
        an instruction rather than an ImportError at module load.
        """
        try:
            import boto3
        except ImportError as error:  # pragma: no cover - depends on the install
            raise ImportError(
                "stow_s3.s3_client needs boto3. Install it with "
                "`pip install stow-s3[boto3]`, or build your own client from "
                "session.endpoint, session.access_key_id and "
                "session.secret_access_key."
            ) from error
        return boto3.client(
            "s3",
            endpoint_url=self.endpoint,
            aws_access_key_id=self.access_key_id,
            aws_secret_access_key=self.secret_access_key,
            region_name=self.region,
            **kwargs,
        )

    def close(self) -> None:
        """Stop the server and delete its data directory.

        Safe to call more than once, and safe to call while the server is already
        gone, because a session must never raise on the way out and mask the
        error that caused the caller to leave.
        """
        if self._closed:
            return
        # The dataclass is frozen because a session describes a server that was
        # already started; lifecycle state is the one thing that must change.
        object.__setattr__(self, "_closed", True)
        _stop_process(self._process)
        shutil.rmtree(self.data_dir, ignore_errors=True)
        for handle in (self._stdout_log, self._stderr_log):
            if handle is not None:
                with contextlib.suppress(OSError, ValueError):
                    handle.close()


def _stop_process(process: subprocess.Popen[bytes]) -> None:
    if process.poll() is not None:
        return
    with contextlib.suppress(OSError):
        process.terminate()
    try:
        process.wait(timeout=STOP_GRACE_SECONDS)
        return
    except subprocess.TimeoutExpired:
        pass
    with contextlib.suppress(OSError):
        process.kill()
    with contextlib.suppress(subprocess.TimeoutExpired):
        process.wait(timeout=STOP_WAIT_SECONDS)


def _read_ready_line(
    read_fd: int,
    process: subprocess.Popen[bytes],
    stdout_log: IO[bytes] | None,
    stderr_log: IO[bytes] | None,
    timeout: float,
) -> str:
    """Read one line from the readiness pipe without letting a hang block us.

    A blocking read on a pipe cannot carry its own timeout, so a reader thread
    is used and the process is left to be killed by the caller's cleanup if it
    never writes. This function owns ``read_fd`` and closes it exactly once.
    """
    captured: list[str] = []
    failure: list[BaseException] = []

    def read() -> None:
        try:
            with os.fdopen(read_fd, "r", encoding="utf-8") as stream:
                captured.append(stream.readline())
        except BaseException as error:  # noqa: BLE001 - surfaced on the main thread
            failure.append(error)

    reader = threading.Thread(target=read, name="stow-ready-reader", daemon=True)
    reader.start()
    reader.join(timeout)
    # The descriptor is closed by the reader's own context manager, so there is
    # deliberately nothing to close here. If the reader is still blocked, closing
    # the descriptor from this thread would race with that read.
    if reader.is_alive():
        raise TimeoutError(
            f"the stow server did not report readiness within {timeout:.0f}s.\n"
            f"stdout:\n{_read_tail(stdout_log)}\n"
            f"stderr:\n{_read_tail(stderr_log)}"
        )
    if failure:
        raise failure[0]
    if not captured or not captured[0].strip():
        raise StowProtocolError(
            "internal",
            "the stow server exited before reporting readiness: "
            f"exit={process.poll()}\nstderr:\n{_read_tail(stderr_log)}",
        )
    return captured[0]


def _server_args(binary: str, data_dir: Path, ready_fd: int, max_bytes: int, max_objects: int) -> list[str]:
    return [
        binary,
        "serve",
        "--port", "0",
        "--host", "127.0.0.1",
        "--data-dir", str(data_dir),
        "--backend", "filesystem",
        "--mode", "local",
        "--max-bytes", str(max_bytes),
        "--max-objects", str(max_objects),
        # Credentials are written to this descriptor rather than stdout, where
        # they would be captured by logs and CI output. The descriptor number is
        # whatever the pipe got: CPython closes inherited descriptors above 2
        # unless they are named in pass_fds, so pinning it to a fixed number
        # would need a preexec_fn hook and would be closed anyway.
        "--ready-fd", str(ready_fd),
        # If this process is killed rather than closed, the server must not
        # outlive it.
        "--parent-pid", str(os.getpid()),
    ]


def open_session(
    *,
    max_bytes: int = DEFAULT_SESSION_MAX_BYTES,
    max_objects: int = DEFAULT_SESSION_MAX_OBJECTS,
    data_dir: str | Path | None = None,
) -> Session:
    """Start a server and return a session that owns it.

    The caller is responsible for calling :meth:`Session.close`, which
    :func:`with_session` does for you.
    """
    if max_bytes < 0 or max_objects < 0:
        raise ValueError("session limits must not be negative")
    binary = require_stow_binary()
    owned_dir: Path | None = None
    if data_dir is None:
        owned_dir = Path(tempfile.mkdtemp(prefix="stow-session-"))
        target = owned_dir
    else:
        target = Path(data_dir)
        target.mkdir(parents=True, exist_ok=True)

    read_fd, write_fd = os.pipe()
    os.set_inheritable(write_fd, True)
    command = _server_args(binary.path, target, write_fd, max_bytes, max_objects)
    started = time.monotonic()
    # The server's own output goes to temporary files rather than pipes. A pipe
    # cannot be read without blocking until the buffer fills or the child exits,
    # so a live server's output would be unreadable exactly when it is most
    # useful. Files also survive the server exiting, for the failure path.
    stdout_log = tempfile.TemporaryFile()
    stderr_log = tempfile.TemporaryFile()
    try:
        process = subprocess.Popen(
            command,
            stdout=stdout_log,
            stderr=stderr_log,
            pass_fds=(write_fd,),
            env=build_child_env(),
        )
    except BaseException:
        os.close(read_fd)
        os.close(write_fd)
        stdout_log.close()
        stderr_log.close()
        if owned_dir is not None:
            shutil.rmtree(owned_dir, ignore_errors=True)
        raise
    finally:
        # The child owns the write end now. Closing our copy is what makes the
        # reader see EOF if the child dies before it writes.
        os.close(write_fd)

    try:
        line = _read_ready_line(read_fd, process, stdout_log, stderr_log, STARTUP_TIMEOUT_SECONDS)
        ready: StowReady = parse_ready_message(line)
        return Session(
            endpoint=ready.endpoint,
            access_key_id=ready.access_key_id,
            secret_access_key=ready.secret_access_key,
            region=ready.region,
            data_dir=target,
            max_bytes=max_bytes,
            max_objects=max_objects,
            mode=ready.mode,
            backend=ready.backend,
            binary_version=ready.binary_version,
            protocol_version=ready.protocol_version,
            capabilities=ready.capabilities,
            _process=process,
            _stdout_log=stdout_log,
            _stderr_log=stderr_log,
        )
    except BaseException as error:
        _stop_process(process)
        for handle in (stdout_log, stderr_log):
            with contextlib.suppress(OSError, ValueError):
                handle.close()
        if owned_dir is not None:
            shutil.rmtree(owned_dir, ignore_errors=True)
        _attach_diagnostics(error, stderr_log, started)
        raise error


def _attach_diagnostics(error: BaseException, stderr_log: IO[bytes] | None, started: float) -> None:
    """Attach the server's own output to a failure.

    A session that will not start produces a stack trace, which is the exact
    thing `stow doctor` and this message exist to improve on.
    """
    detail = _read_tail(stderr_log).strip()
    if not detail:
        return
    error.add_note(
        f"the stow server reported after {time.monotonic() - started:.1f}s:\n{detail}"
    )


@contextlib.contextmanager
def with_session(
    *,
    max_bytes: int = DEFAULT_SESSION_MAX_BYTES,
    max_objects: int = DEFAULT_SESSION_MAX_OBJECTS,
    data_dir: str | Path | None = None,
) -> Iterator[Session]:
    """Run a session for the duration of a block, then always clean up."""
    session = open_session(max_bytes=max_bytes, max_objects=max_objects, data_dir=data_dir)
    try:
        yield session
    finally:
        session.close()
