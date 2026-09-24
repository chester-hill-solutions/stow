#!/usr/bin/env node
import { execFileSync } from "node:child_process";
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { resolve } from "node:path";
import { readPreviousBaseline } from "./baseline-history.mjs";

const repoRoot = resolve(import.meta.dirname, "..");
const baselinePath = resolve(repoRoot, "scripts/baselines/go-coverage.json");
const profilePath = resolve(repoRoot, ".cache/go-coverage.out");
mkdirSync(resolve(repoRoot, ".cache"), { recursive: true });
execFileSync("go", ["test", "./...", "-coverprofile", profilePath], { cwd: repoRoot, stdio: "inherit" });
const report = execFileSync("go", ["tool", "cover", "-func", profilePath], { cwd: repoRoot, encoding: "utf8" });
const match = report.match(/total:\s+\(statements\)\s+([0-9.]+)%/);
if (!match) {
  console.error("Could not parse Go coverage total");
  process.exit(2);
}
const current = Number(match[1]);
if (process.argv.includes("--baseline")) {
  writeFileSync(baselinePath, `${JSON.stringify({ percentage: current }, null, 2)}\n`);
  console.log(`Wrote ${baselinePath}: ${current}%`);
  process.exit(0);
}
if (!existsSync(baselinePath)) {
  console.error(`Coverage baseline missing: ${baselinePath}`);
  process.exit(2);
}
const baseline = JSON.parse(readFileSync(baselinePath, "utf8"));
let previous = null;
try {
  previous = readPreviousBaseline(repoRoot, "scripts/baselines/go-coverage.json");
} catch (error) {
  console.error(error.message);
  process.exit(2);
}
if (previous && baseline.percentage < previous.percentage) {
  console.error("Go coverage baseline was lowered without a recorded improvement");
  process.exit(1);
}
if (current < baseline.percentage) {
  console.error(`Go coverage ratchet violation: ${current}% < ${baseline.percentage}%`);
  process.exit(1);
}
if (current > baseline.percentage) {
  console.error(`Go coverage baseline stale: ${current}% > ${baseline.percentage}%; raise the baseline after verifying the improvement`);
  process.exit(1);
}
console.log(`Go coverage ratchet OK (${current}%)`);
