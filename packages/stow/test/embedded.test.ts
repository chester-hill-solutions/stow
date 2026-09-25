import assert from "node:assert/strict";
import { describe, it } from "node:test";
import {
  EmbeddedStow,
  EmbeddedStowError,
  type EmbeddedHost,
} from "../dist/index.js";

class FakeHost implements EmbeddedHost {
  readonly requests: Record<string, unknown>[] = [];

  call(request: string): string {
    const parsed = JSON.parse(request) as Record<string, unknown>;
    this.requests.push(parsed);
    switch (parsed.op) {
      case "open":
        return response({
          handle: 7,
          capabilities: {
            backend: "memory",
            maxBytes: 10,
            maxObjects: 2,
            persistent: false,
            multipart: false,
            upstream: false,
          },
        });
      case "getObject":
        return response({
          bucket: parsed.bucket,
          key: parsed.key,
          data: "aGVsbG8=",
          size: 5,
          etag: "etag",
          metadata: { owner: "test" },
        });
      case "putObject":
        return response({
          bucket: parsed.bucket,
          key: parsed.key,
          size: 5,
          etag: "etag",
        });
      case "listBuckets":
        return response({ buckets: [{ name: "assets" }] });
      case "listObjects":
        return response({ objects: [{ bucket: "assets", key: "hello.txt", size: 5, etag: "etag" }] });
      case "usage":
        return response({ bytes: 5, objects: 1 });
      case "copyObject":
        return response({ bucket: "assets", key: "copy.txt", size: 5, etag: "etag" });
      default:
        return response(undefined);
    }
  }
}

function response(result: unknown, version = 1): string {
  return JSON.stringify({ version, ok: true, result });
}

describe("EmbeddedStow", () => {
  it("adapts the host bridge with byte-safe object operations", () => {
    const host = new FakeHost();
    const stow = EmbeddedStow.open(host, { maxBytes: 10, maxObjects: 2 });

    assert.equal(stow.handle, 7);
    assert.equal(stow.capabilities().backend, "memory");
    stow.createBucket("assets");
    assert.equal(host.requests[0]?.version, 1);
    assert.equal(host.requests[1]?.handle, 7);
    stow.putObject("assets", "hello.txt", new TextEncoder().encode("hello"), {
      contentType: "text/plain",
      metadata: { owner: "test" },
    });
    const object = stow.getObject("assets", "hello.txt");
    assert.deepEqual(object.data, new TextEncoder().encode("hello"));
    assert.equal(object.metadata?.owner, "test");
    assert.deepEqual(stow.listBuckets(), [{ name: "assets" }]);
    assert.equal(stow.listObjects("assets")[0]?.key, "hello.txt");
    assert.deepEqual(stow.usage(), { bytes: 5, objects: 1 });
    assert.equal(stow.copyObject("assets", "hello.txt", "assets", "copy.txt").key, "copy.txt");
    stow.reset();
    stow.close();
    stow.close();
    assert.throws(() => stow.getObject("assets", "hello.txt"), EmbeddedStowError);
  });

  it("rejects protocol version mismatches", () => {
    const host: EmbeddedHost = {
      call: () => response({ handle: 7, capabilities: {} }, 2),
    };
    assert.throws(
      () => EmbeddedStow.open(host),
      (error: unknown) => hasCode(error, "protocol_version"),
    );
  });

  it("preserves structured bridge error codes", () => {
    const host: EmbeddedHost = {
      call: (request) => {
        const parsed = JSON.parse(request) as { op?: string };
        if (parsed.op === "open") {
          return response({ handle: 7, capabilities: {} });
        }
        return JSON.stringify({
          version: 1,
          ok: false,
          error: { code: "quota_exceeded", message: "quota exceeded" },
        });
      },
    };
    const stow = EmbeddedStow.open(host);
    assert.throws(
      () => stow.createBucket("assets"),
      (error: unknown) => hasCode(error, "quota_exceeded"),
    );
  });
});

function hasCode(error: unknown, code: string): boolean {
  return error instanceof EmbeddedStowError && Reflect.get(error, "code") === code;
}
