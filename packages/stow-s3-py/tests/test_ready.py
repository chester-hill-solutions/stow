"""Tests for the readiness protocol parser.

These run without the native binary, because protocol parsing is worth testing
on its own: a bug here is the difference between a clear error and a session
that half-works.
"""

from __future__ import annotations

import json

import pytest

from stow_s3 import READY_PROTOCOL_VERSION, StowProtocolError, parse_ready_message


def ready_payload(**overrides: object) -> str:
    message = {
        "protocolVersion": READY_PROTOCOL_VERSION,
        "binaryVersion": "0.2.0",
        "endpoint": "http://127.0.0.1:9000",
        "region": "us-east-1",
        "accessKeyId": "AKIAEXAMPLE",
        "secretAccessKey": "secret",
        "mode": "local",
        "backend": "filesystem",
        "capabilities": {
            "persistent": True,
            "multipart": True,
            "upstream": False,
            "conditionalWrites": True,
            "presignedUrls": True,
            "maxBytes": 16 * 1024 * 1024,
            "maxObjects": 1000,
            "maxRequestBytes": 8 * 1024 * 1024,
        },
    }
    message.update(overrides)
    return json.dumps(message)


def test_parses_a_complete_message() -> None:
    ready = parse_ready_message(ready_payload())
    assert ready.endpoint == "http://127.0.0.1:9000"
    assert ready.backend == "filesystem"
    assert ready.capabilities.max_bytes == 16 * 1024 * 1024
    assert ready.capabilities.max_objects == 1000
    assert ready.capabilities.upstream is False


def test_rejects_a_different_protocol_version() -> None:
    with pytest.raises(StowProtocolError) as caught:
        parse_ready_message(ready_payload(protocolVersion=99))
    assert caught.value.code == "protocol_mismatch"
    assert "99" in str(caught.value)
    assert str(READY_PROTOCOL_VERSION) in str(caught.value)


def test_rejects_invalid_json() -> None:
    with pytest.raises(StowProtocolError) as caught:
        parse_ready_message("not json at all")
    assert caught.value.code == "protocol_mismatch"


def test_rejects_a_json_array() -> None:
    with pytest.raises(StowProtocolError):
        parse_ready_message("[1, 2, 3]")


@pytest.mark.parametrize(
    "field",
    ["binaryVersion", "endpoint", "region", "accessKeyId", "secretAccessKey", "mode", "backend"],
)
def test_rejects_a_missing_string_field(field: str) -> None:
    message = json.loads(ready_payload())
    del message[field]
    with pytest.raises(StowProtocolError) as caught:
        parse_ready_message(json.dumps(message))
    assert field in str(caught.value)


def test_rejects_missing_capabilities() -> None:
    message = json.loads(ready_payload())
    del message["capabilities"]
    with pytest.raises(StowProtocolError) as caught:
        parse_ready_message(json.dumps(message))
    assert "capabilities" in str(caught.value)


def test_rejects_a_boolean_where_a_number_is_required() -> None:
    # bool is a subclass of int in Python, so a naive isinstance check would let
    # True through as the number 1 and the caller would get a nonsense limit.
    message = json.loads(ready_payload())
    message["capabilities"]["maxBytes"] = True
    with pytest.raises(StowProtocolError) as caught:
        parse_ready_message(json.dumps(message))
    assert "maxBytes" in str(caught.value)


def test_rejects_a_number_where_a_boolean_is_required() -> None:
    message = json.loads(ready_payload())
    message["capabilities"]["persistent"] = 1
    with pytest.raises(StowProtocolError) as caught:
        parse_ready_message(json.dumps(message))
    assert "persistent" in str(caught.value)


def test_rejects_an_empty_string_field() -> None:
    message = json.loads(ready_payload())
    message["endpoint"] = ""
    with pytest.raises(StowProtocolError) as caught:
        parse_ready_message(json.dumps(message))
    assert "endpoint" in str(caught.value)


def test_zero_limits_are_carried_through_unchanged() -> None:
    # 0 means the server reported no limit. It must not be confused with a
    # missing field or turned into None, because a caller cannot tell those
    # apart and would guess.
    message = json.loads(ready_payload())
    message["capabilities"]["maxBytes"] = 0
    message["capabilities"]["maxObjects"] = 0
    ready = parse_ready_message(json.dumps(message))
    assert ready.capabilities.max_bytes == 0
    assert ready.capabilities.max_objects == 0
