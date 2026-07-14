import assert from "node:assert/strict";
import { mkdtemp } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { describe, it } from "node:test";
import { stowBinaryAvailable } from "../dist/bin.js";
import { verifyObjectReadable } from "../dist/instance.js";
import { Stow } from "../dist/index.js";
import { parseReadyLine } from "../dist/start.js";

describe("parseReadyLine", () => {
  it("parses the STOW_READY banner line", () => {
    const ready = parseReadyLine(
      "STOW_READY endpoint=http://127.0.0.1:54321 access_key=ABCDEF secret_key=ghijklmnop mode=local",
    );
    assert.equal(ready?.endpoint, "http://127.0.0.1:54321");
    assert.equal(ready?.accessKeyId, "ABCDEF");
    assert.equal(ready?.secretAccessKey, "ghijklmnop");
    assert.equal(ready?.mode, "local");
  });
});

const integration = describe;
integration("integration", { skip: !stowBinaryAvailable() }, () => {
  it("starts, writes, reads, and stops", async () => {
    const dataDir = await mkdtemp(join(tmpdir(), "stow-test-"));
    const instance = await Stow.start({
      dataDir,
      buckets: ["uploads"],
      port: 0,
      cleanSlate: true,
    });

    try {
      assert.match(instance.endpoint, /^http:\/\/127\.0\.0\.1:\d+$/);
      assert.ok(instance.accessKeyId.length > 0);
      assert.ok(instance.secretAccessKey.length > 0);

      const config = instance.awsSdkV3Config();
      assert.equal(config.endpoint, instance.endpoint);
      assert.equal(config.forcePathStyle, true);

      await instance.putFixture("uploads", "hello.txt", "hello world");
      const body = await verifyObjectReadable(instance, "uploads", "hello.txt");
      assert.equal(body, "hello world");

      const snapshot = await instance.snapshotObjects("uploads");
      assert.deepEqual(snapshot.map((item) => item.key), ["hello.txt"]);
    } finally {
      await instance.stop();
    }
  });
});
