import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFile } from "node:fs/promises";
import { join } from "node:path";
import { setTimeout as delay } from "node:timers/promises";
import { test } from "node:test";
import vm from "node:vm";

const goroot = execFileSync("go", ["env", "GOROOT"], { encoding: "utf8" }).trim();
vm.runInThisContext(await readFile(join(goroot, "lib/wasm/wasm_exec.js"), "utf8"));

const go = new Go();
const wasmBytes = await readFile(new URL("../bin/stow-runtime.wasm", import.meta.url));
const { instance } = await WebAssembly.instantiate(wasmBytes, go.importObject);
const runPromise = go.run(instance);
for (let attempt = 0; attempt < 100 && !globalThis.stow; attempt += 1) {
  await delay(1);
}
assert.ok(globalThis.stow, "WASM runtime did not initialize");

function call(request) {
  const response = JSON.parse(globalThis.stow.call(JSON.stringify(request)));
  assert.equal(response.ok, true, response.error);
  return response;
}

test("memory runtime is callable from the WASM host", async () => {
  try {
  const opened = call({ op: "open", options: { maxBytes: 10, maxObjects: 2 } });
  const handle = opened.result.handle;
  call({ op: "createBucket", handle, bucket: "assets" });
  const put = call({
    op: "putObject",
    handle,
    bucket: "assets",
    key: "hello.txt",
    data: Buffer.from("hello").toString("base64"),
  });
  assert.equal(put.result.size, 5);

  const got = call({ op: "getObject", handle, bucket: "assets", key: "hello.txt" });
  assert.equal(Buffer.from(got.result.data, "base64").toString(), "hello");
  const listed = call({ op: "listObjects", handle, bucket: "assets", options: {} });
  assert.deepEqual(listed.result.objects.map((object) => object.key), ["hello.txt"]);
  const buckets = call({ op: "listBuckets", handle });
  assert.deepEqual(buckets.result.buckets.map((bucket) => bucket.name), ["assets"]);
  call({
    op: "copyObject",
    handle,
    sourceBucket: "assets",
    sourceKey: "hello.txt",
    destinationBucket: "assets",
    destinationKey: "copy.txt",
  });

  const quota = JSON.parse(globalThis.stow.call(JSON.stringify({
    op: "putObject",
    handle,
    bucket: "assets",
    key: "too-large",
    data: Buffer.from("123456").toString("base64"),
  })));
  assert.equal(quota.ok, false);
  assert.match(quota.error, /quota/i);

  call({ op: "reset", handle });
  const resetUsage = call({ op: "usage", handle });
  assert.deepEqual(resetUsage.result, { bytes: 0, objects: 0 });
  call({ op: "close", handle });
  } finally {
    globalThis.stow.exit();
    await runPromise;
  }
});
