import assert from "node:assert/strict";
import { describe, it } from "node:test";
import {
  READY_PROTOCOL_VERSION,
  StowProtocolError,
  parseReadyMessage,
} from "../dist/ready.js";

const VALID = {
  protocolVersion: READY_PROTOCOL_VERSION,
  binaryVersion: "0.2.0",
  endpoint: "http://127.0.0.1:43127",
  region: "us-east-1",
  accessKeyId: "generated-access-key",
  secretAccessKey: "generated-secret-key",
  mode: "local",
  backend: "memory",
  capabilities: {
    persistent: false,
    multipart: true,
    upstream: false,
    conditionalWrites: true,
    presignedUrls: true,
    maxBytes: 16777216,
    maxObjects: 1000,
    maxRequestBytes: 8388608,
  },
};

describe("ready protocol", () => {
  it("parses a complete message", () => {
    const message = parseReadyMessage(JSON.stringify(VALID));
    assert.equal(message.endpoint, VALID.endpoint);
    assert.equal(message.mode, "local");
    assert.equal(message.capabilities.maxBytes, 16777216);
    assert.equal(message.capabilities.persistent, false);
  });

  it("rejects an unknown protocol version with a specific error", () => {
    assert.throws(
      () => parseReadyMessage(JSON.stringify({ ...VALID, protocolVersion: 99 })),
      (error: unknown) => {
        assert.ok(error instanceof StowProtocolError);
        assert.equal(error.code, "protocol_mismatch");
        assert.match(error.message, /99/);
        return true;
      },
    );
  });

  it("rejects malformed JSON and non-objects", () => {
    for (const payload of ["not json", "[]", '"a string"', "42"]) {
      assert.throws(() => parseReadyMessage(payload), StowProtocolError, payload);
    }
  });

  it("rejects a message missing a required field", () => {
    for (const field of [
      "endpoint",
      "region",
      "accessKeyId",
      "secretAccessKey",
      "mode",
      "backend",
      "binaryVersion",
    ]) {
      const partial: Record<string, unknown> = { ...VALID };
      delete partial[field];
      assert.throws(
        () => parseReadyMessage(JSON.stringify(partial)),
        (error: unknown) => {
          assert.ok(error instanceof StowProtocolError);
          assert.match(error.message, new RegExp(field));
          return true;
        },
        `expected ${field} to be required`,
      );
    }
  });

  it("rejects a message missing capabilities", () => {
    const partial: Record<string, unknown> = { ...VALID };
    delete partial.capabilities;
    assert.throws(() => parseReadyMessage(JSON.stringify(partial)), StowProtocolError);
  });

  it("rejects a capability with the wrong type", () => {
    const broken = {
      ...VALID,
      capabilities: { ...VALID.capabilities, multipart: "yes" },
    };
    assert.throws(() => parseReadyMessage(JSON.stringify(broken)), (error: unknown) => {
      assert.ok(error instanceof StowProtocolError);
      assert.match(error.message, /multipart/);
      return true;
    });
  });
});
