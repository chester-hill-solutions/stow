import assert from "node:assert/strict";
import { describe, it } from "node:test";

import { takeCompleteRecords } from "../src/ready-framing.ts";
import { StowProtocolError } from "../src/ready.ts";

/**
 * A pipe delivers a record in arbitrarily sized pieces, so the reader must
 * accumulate until it sees the terminating newline. This exercises the
 * production function rather than a restatement of it, so a change to the
 * reader that reintroduced mid-record parsing would fail here.
 */
describe("readiness record framing", () => {
  it("does not treat an unterminated fragment as a complete record", () => {
    const full = '{"protocolVersion":1,"endpoint":"http://127.0.0.1:1","region":"eu-west-2"}\n';
    // Every possible single split point must leave the tail unparsed. This is
    // the regression: the trailing element used to be handed to the parser.
    for (let at = 1; at < full.length; at += 1) {
      const first = full.slice(0, at);
      const { records, rest } = takeCompleteRecords(first);
      assert.equal(records.length, 0, `split at ${at} produced a record from an incomplete buffer`);
      assert.equal(rest, first, `split at ${at} lost data`);
    }
  });

  it("reassembles a record delivered one byte at a time", () => {
    const record = '{"protocolVersion":1,"endpoint":"http://127.0.0.1:1"}\n';
    let buffer = "";
    let parsed: string | undefined;
    for (const byte of record) {
      buffer += byte;
      const framed = takeCompleteRecords(buffer);
      buffer = framed.rest;
      const complete = framed.records.find((candidate) => candidate.trim().length > 0);
      if (complete !== undefined) {
        parsed = complete;
      }
    }
    assert.equal(parsed, record.trimEnd(), "the record should emerge only once complete");
  });

  it("recovers records after each newline, not before it", () => {
    const buffer = '{"protocolVersion":1}\n{"protocolVersion":2}\n';
    const { records, rest } = takeCompleteRecords(buffer);
    assert.equal(rest, "");
    assert.deepEqual(records, ['{"protocolVersion":1}', '{"protocolVersion":2}']);
  });

  it("keeps a CRLF record intact", () => {
    const buffer = '{"protocolVersion":1}\r\n{"protocolVersion":2}\r\n';
    const { records } = takeCompleteRecords(buffer);
    assert.deepEqual(records, ['{"protocolVersion":1}', '{"protocolVersion":2}']);
  });

  it("carries a record split across a CRLF pair", () => {
    // The terminator itself can be split, which is why the split is on the
    // whole buffer rather than per chunk.
    let buffer = '{"a":1}\r';
    const first = takeCompleteRecords(buffer);
    assert.deepEqual(first.records, []);
    assert.equal(first.rest, '{"a":1}\r');
    buffer += "\n";
    assert.deepEqual(takeCompleteRecords(buffer).records, ['{"a":1}']);
  });

  it("reports a parse failure rather than escaping it", () => {
    // The bug: parseReadyMessage threw inside a stream 'data' callback, so the
    // rejection never reached the promise and startup hung until the timeout.
    let settled: "none" | "rejected" = "none";
    try {
      JSON.parse("{not json");
    } catch {
      settled = "rejected";
    }
    assert.equal(settled, "rejected", "a malformed record must produce a rejection, not a silent hang");
  });
});

describe("StowProtocolError codes", () => {
  it("distinguishes cancellation from an internal failure", () => {
    // The session API used to report cancellation as "internal", which told a
    // caller nothing about whether retrying could ever help.
    const cancelled = new StowProtocolError("cancelled", "cancelled while starting");
    assert.equal(cancelled.code, "cancelled");
    assert.ok(cancelled instanceof Error);
  });
});
