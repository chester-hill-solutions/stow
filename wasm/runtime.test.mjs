import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFile } from "node:fs/promises";
import { join } from "node:path";
import { setTimeout as delay } from "node:timers/promises";
import { test } from "node:test";
import vm from "node:vm";

const goroot = execFileSync("go", ["env", "GOROOT"], { encoding: "utf8" }).trim();
vm.runInThisContext(await readFile(join(goroot, "lib/wasm/wasm_exec.js"), "utf8"));

const { EmbeddedStow, EmbeddedStowError } = await import(
  new URL("../packages/stow-s3/dist/embedded.js", import.meta.url)
);

const go = new Go();
const wasmBytes = await readFile(new URL("../bin/stow-runtime.wasm", import.meta.url));
const { instance } = await WebAssembly.instantiate(wasmBytes, go.importObject);
const runPromise = go.run(instance);
for (let attempt = 0; attempt < 100 && !globalThis.stow; attempt += 1) {
  await delay(1);
}
assert.ok(globalThis.stow, "WASM runtime did not initialize");

test("EmbeddedStow drives the real memory runtime", async () => {
  const host = { call: (request) => globalThis.stow.call(request) };
  let embedded;
  try {
    embedded = EmbeddedStow.open(host, { maxBytes: 10, maxObjects: 2 });
    embedded.createBucket("assets");
    const put = embedded.putObject(
      "assets",
      "hello.txt",
      new TextEncoder().encode("hello"),
    );
    assert.equal(put.size, 5);

    const got = embedded.getObject("assets", "hello.txt");
    assert.deepEqual(got.data, new TextEncoder().encode("hello"));
    const listed = embedded.listObjects("assets", { limit: 1 });
    assert.equal(listed.truncated, false);
    assert.deepEqual(listed.objects.map((object) => object.key), ["hello.txt"]);
    assert.deepEqual(
      embedded.listBuckets().map((bucket) => bucket.name),
      ["assets"],
    );
    embedded.copyObject("assets", "hello.txt", "assets", "copy.txt");

    assert.throws(
      () => embedded.putObject("assets", "too-large", new TextEncoder().encode("123456")),
      /quota/i,
    );
    assert.deepEqual(embedded.usage(), { bytes: 10, objects: 2 });
    embedded.reset();
    assert.deepEqual(embedded.usage(), { bytes: 0, objects: 0 });
    embedded.close();
    assert.throws(() => embedded.usage(), EmbeddedStowError);
  } finally {
    try {
      embedded?.close();
    } catch {
      // The test may fail before the public close operation is reached.
    }
    globalThis.stow.exit();
    await runPromise;
  }
});
