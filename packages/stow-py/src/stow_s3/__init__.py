"""Python client for the stow local S3-compatible dev bucket service.

A session is a disposable, fully isolated S3 endpoint. It starts a native server
as a child process, reports the endpoint and credentials over a versioned
readiness channel rather than on stdout, and guarantees the server is gone when
the session closes.

    from stow_s3 import with_session

    with with_session() as session:
        s3 = session.s3_client()
        bucket = session.new_bucket_name()
        s3.create_bucket(Bucket=bucket)
        s3.put_object(Bucket=bucket, Key="input.json", Body=b'{"task":"summarize"}')

``boto3`` is an optional extra. Install it with ``pip install stow-s3[boto3]``,
or build a client from any S3 library using :attr:`Session.endpoint`,
:attr:`Session.access_key_id` and :attr:`Session.secret_access_key`.
"""

from .bin import (
    BinarySource,
    ResolvedBinary,
    StowBinaryNotFoundError,
    describe_source,
    require_stow_binary,
    resolve_stow_binary,
    resolve_stow_binary_detailed,
    stow_binary_available,
)
from .ready import (
    READY_PROTOCOL_VERSION,
    StowCapabilities,
    StowProtocolError,
    StowReady,
    parse_ready_message,
)
from .session import (
    DEFAULT_SESSION_MAX_BYTES,
    DEFAULT_SESSION_MAX_OBJECTS,
    Session,
    build_child_env,
    open_session,
    with_session,
)

__version__ = "0.2.0"

__all__ = [
    "BinarySource",
    "DEFAULT_SESSION_MAX_BYTES",
    "DEFAULT_SESSION_MAX_OBJECTS",
    "READY_PROTOCOL_VERSION",
    "ResolvedBinary",
    "Session",
    "StowBinaryNotFoundError",
    "StowCapabilities",
    "StowProtocolError",
    "StowReady",
    "__version__",
    "build_child_env",
    "describe_source",
    "open_session",
    "parse_ready_message",
    "require_stow_binary",
    "resolve_stow_binary",
    "resolve_stow_binary_detailed",
    "stow_binary_available",
    "with_session",
]
