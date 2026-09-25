"""Client half of the session readiness protocol.

The server writes one JSON object to a file descriptor. Parsing is strict on
purpose: an unknown protocol version or a missing field is a specific error, not
a partially populated session that fails later in a confusing place.

This is the same protocol the TypeScript client speaks. The two must agree on the
message shape, the protocol version, and the meaning of a zero limit.
"""

from __future__ import annotations

import json
from dataclasses import dataclass
from typing import Any

# The version of this message shape. A client rejects an unknown version rather
# than guessing, because a later version may add or change fields.
READY_PROTOCOL_VERSION = 1

PROTOCOL_MISMATCH = "protocol_mismatch"
INTERNAL = "internal"


class StowProtocolError(Exception):
    """A ready message this client cannot safely act on.

    The ``code`` mirrors the TypeScript client so a caller that moves between
    languages sees the same distinction: a version mismatch is something to
    report or upgrade around, while ``internal`` is a bug here.
    """

    def __init__(self, code: str, message: str) -> None:
        super().__init__(message)
        self.code = code


@dataclass(frozen=True)
class StowCapabilities:
    """What a caller may rely on before issuing operations."""

    persistent: bool
    multipart: bool
    upstream: bool
    conditional_writes: bool
    presigned_urls: bool
    # A limit of 0 means the server reported no limit, which is distinct from a
    # limit that happens to be zero. It is not "unlimited" and not "unknown";
    # it is what the protocol says when nothing was reported.
    max_bytes: int
    max_objects: int
    max_request_bytes: int


@dataclass(frozen=True)
class StowReady:
    """The single readiness object the server writes."""

    protocol_version: int
    binary_version: str
    endpoint: str
    region: str
    access_key_id: str
    secret_access_key: str
    mode: str
    backend: str
    capabilities: StowCapabilities


def _require_string(source: dict[str, Any], field: str) -> str:
    value = source.get(field)
    if not isinstance(value, str) or not value:
        raise StowProtocolError(PROTOCOL_MISMATCH, f"ready message is missing {field}")
    return value


def _require_bool(source: dict[str, Any], field: str) -> bool:
    # isinstance alone is not enough to catch a number, because bool is a
    # subclass of int and a truthy 1 would otherwise pass as True.
    if not isinstance(source.get(field), bool):
        raise StowProtocolError(PROTOCOL_MISMATCH, f"ready message is missing {field}")
    return source[field]


def _require_number(source: dict[str, Any], field: str) -> float:
    value = source.get(field)
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        raise StowProtocolError(PROTOCOL_MISMATCH, f"ready message is missing {field}")
    if isinstance(value, float) and (value != value or value in (float("inf"), float("-inf"))):
        raise StowProtocolError(PROTOCOL_MISMATCH, f"ready message is missing {field}")
    return value


def parse_ready_message(line: str) -> StowReady:
    """Parse the readiness object.

    Raises :class:`StowProtocolError` on an unknown protocol version, a
    non-object payload, or any missing field.
    """
    try:
        decoded = json.loads(line)
    except json.JSONDecodeError as error:
        raise StowProtocolError(
            PROTOCOL_MISMATCH, f"ready message is not valid JSON: {error}"
        ) from error
    if not isinstance(decoded, dict):
        raise StowProtocolError(PROTOCOL_MISMATCH, "ready message is not a JSON object")

    version = _require_number(decoded, "protocolVersion")
    if version != READY_PROTOCOL_VERSION:
        raise StowProtocolError(
            PROTOCOL_MISMATCH,
            f"unsupported ready protocol version {version}; "
            f"this client speaks {READY_PROTOCOL_VERSION}",
        )

    capabilities = decoded.get("capabilities")
    if not isinstance(capabilities, dict):
        raise StowProtocolError(PROTOCOL_MISMATCH, "ready message is missing capabilities")

    return StowReady(
        protocol_version=int(version),
        binary_version=_require_string(decoded, "binaryVersion"),
        endpoint=_require_string(decoded, "endpoint"),
        region=_require_string(decoded, "region"),
        access_key_id=_require_string(decoded, "accessKeyId"),
        secret_access_key=_require_string(decoded, "secretAccessKey"),
        mode=_require_string(decoded, "mode"),
        backend=_require_string(decoded, "backend"),
        capabilities=StowCapabilities(
            persistent=_require_bool(capabilities, "persistent"),
            multipart=_require_bool(capabilities, "multipart"),
            upstream=_require_bool(capabilities, "upstream"),
            conditional_writes=_require_bool(capabilities, "conditionalWrites"),
            presigned_urls=_require_bool(capabilities, "presignedUrls"),
            max_bytes=int(_require_number(capabilities, "maxBytes")),
            max_objects=int(_require_number(capabilities, "maxObjects")),
            max_request_bytes=int(_require_number(capabilities, "maxRequestBytes")),
        ),
    )
