import assert from "node:assert/strict";
import { access } from "node:fs/promises";
import { test } from "node:test";

import { EmbeddedStow, EmbeddedStowError } from "../dist/index.js";
import { loadNodeWasmHost } from "../dist/node-wasm-host.js";

test("loadNodeWasmHost starts the packaged memory runtime", async (t) => {
  try {
    await access(new URL("../dist/stow-runtime.wasm", import.meta.url));
  } catch {
    if (process.env.CI) {
      throw new Error("packaged WASM asset is missing in CI");
    }
    t.skip("WASM asset is built by the WASM test target");
    return;
  }

  const host = await loadNodeWasmHost();
  const runtime = EmbeddedStow.open(host, { maxBytes: 8, maxObjects: 2 });
  try {
    runtime.createBucket("package");
    runtime.putObject("package", "hello.txt", new TextEncoder().encode("hello"));
    assert.deepEqual(
      runtime.getObject("package", "hello.txt").data,
      new TextEncoder().encode("hello"),
    );
    runtime.close();
    assert.throws(() => runtime.usage(), EmbeddedStowError);
  } finally {
    try {
      runtime.close();
    } catch {
      // The public close assertion may have completed already.
    }
    await host.close();
  }
});
