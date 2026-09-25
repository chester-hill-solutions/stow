import { execFile } from "node:child_process";
import { access, chmod, copyFile, mkdir } from "node:fs/promises";
import { dirname, join } from "node:path";
import process from "node:process";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";

const execFileAsync = promisify(execFile);
const packageRoot = dirname(dirname(fileURLToPath(import.meta.url)));
const distDir = join(packageRoot, "dist");
const wasmSource = join(packageRoot, "..", "..", "bin", "stow-runtime.wasm");

// copyFile carries the source mode across, and the two places these assets come
// from disagree about what that mode is: the tarball toolchain ships
// wasm_exec.js as 0755, the module-cache toolchain as 0444. Git records only the
// executable bit, so on a fresh checkout the copy gains it and check-generated
// reports a difference that has nothing to do with the source. Set the mode
// here instead of inheriting it, so the committed mode holds either way.
const ASSET_MODE = 0o644;

export async function copyWasmAssets({ dist, wasm, wasmExec }) {
  await copyFile(wasm, join(dist, "stow-runtime.wasm"));
  await copyFile(wasmExec, join(dist, "wasm_exec.js"));
  await chmod(join(dist, "stow-runtime.wasm"), ASSET_MODE);
  await chmod(join(dist, "wasm_exec.js"), ASSET_MODE);
}

export async function locateWasmExec() {
  const { stdout: goroot } = await execFileAsync("go", ["env", "GOROOT"]);
  return join(goroot.trim(), "lib", "wasm", "wasm_exec.js");
}

async function main() {
  await mkdir(distDir, { recursive: true });
  try {
    await access(wasmSource);
  } catch {
    process.stdout.write("stow wasm asset not built; skipping package copy\n");
    return;
  }
  await copyWasmAssets({ dist: distDir, wasm: wasmSource, wasmExec: await locateWasmExec() });
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  await main();
}
