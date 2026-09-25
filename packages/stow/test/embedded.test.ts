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

function response(result: unknown): string {
  return JSON.stringify({ ok: true, result });
}

describe("EmbeddedStow", () => {
  it("adapts the host bridge with byte-safe object operations", () => {
    const host = new FakeHost();
    const stow = EmbeddedStow.open(host, { maxBytes: 10, maxObjects: 2 });

    assert.equal(stow.handle, 7);
    assert.equal(stow.capabilities().backend, "memory");
    stow.createBucket("assets");
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
});
