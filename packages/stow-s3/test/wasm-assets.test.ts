import assert from "node:assert/strict";
import { chmod, mkdtemp, stat, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";

import { copyWasmAssets } from "../scripts/copy-wasm-assets.mjs";

async function stageAsset(dir: string, name: string, mode: number): Promise<string> {
  const path = join(dir, name);
  await writeFile(path, name);
  await chmod(path, mode);
  return path;
}

function modeOf(path: string): Promise<number> {
  return stat(path).then((info) => info.mode & 0o777);
}

// 0755 is how the tarball toolchain ships wasm_exec.js; 0444 is how the
// module-cache toolchain ships it. CI installs from the tarball and a local
// GOTOOLCHAIN build comes from the module cache, so the copy has to produce
// the committed mode from either, or check-generated fails on a fresh
// checkout and passes in the working tree.
test("copied wasm assets take a fixed mode regardless of the source mode", async () => {
  for (const sourceMode of [0o755, 0o444, 0o644]) {
    const dist = await mkdtemp(join(tmpdir(), "stow-dist-"));
    const sources = await mkdtemp(join(tmpdir(), "stow-asset-src-"));
    const wasm = await stageAsset(sources, "stow-runtime.wasm", 0o755);
    const wasmExec = await stageAsset(sources, "wasm_exec.js", sourceMode);

    await copyWasmAssets({ dist, wasm, wasmExec });

    const from = `source mode ${sourceMode.toString(8)}`;
    assert.equal(await modeOf(join(dist, "stow-runtime.wasm")), 0o644, `wasm copied from ${from}`);
    assert.equal(await modeOf(join(dist, "wasm_exec.js")), 0o644, `wasm_exec.js copied from ${from}`);
  }
});
