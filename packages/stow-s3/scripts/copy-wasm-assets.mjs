import { execFile } from "node:child_process";
import { access, copyFile, mkdir } from "node:fs/promises";
import { dirname, join } from "node:path";
import process from "node:process";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";

const execFileAsync = promisify(execFile);
const packageRoot = dirname(dirname(fileURLToPath(import.meta.url)));
const distDir = join(packageRoot, "dist");
const wasmSource = join(packageRoot, "..", "..", "bin", "stow-runtime.wasm");

await mkdir(distDir, { recursive: true });
try {
  await access(wasmSource);
} catch {
  process.stdout.write("stow wasm asset not built; skipping package copy\n");
  process.exit(0);
}

const { stdout: goroot } = await execFileAsync("go", ["env", "GOROOT"]);
const wasmExecSource = join(goroot.trim(), "lib", "wasm", "wasm_exec.js");
await copyFile(wasmExecSource, join(distDir, "wasm_exec.js"));
await copyFile(wasmSource, join(distDir, "stow-runtime.wasm"));
