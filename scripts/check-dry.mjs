#!/usr/bin/env node
import { execFileSync } from "node:child_process";
import { existsSync, readFileSync, writeFileSync } from "node:fs";
import { resolve } from "node:path";
import { readPreviousBaseline } from "./baseline-history.mjs";

const repoRoot = resolve(import.meta.dirname, "..");
const packageRoot = resolve(repoRoot, "packages/stow");
const baselinePath = resolve(repoRoot, "scripts/baselines/dry.json");
const reportPath = resolve(packageRoot, "node_modules/.cache/jscpd/jscpd-report.json");

try {
  execFileSync(resolve(packageRoot, "node_modules/.bin/jscpd"), [], { cwd: packageRoot, stdio: "ignore" });
} catch {
  // jscpd may return non-zero when it finds clones; its report is still useful.
}
if (!existsSync(reportPath)) {
  console.error(`DRY report not found: ${reportPath}`);
  process.exit(2);
}
const statistics = JSON.parse(readFileSync(reportPath, "utf8")).statistics.total;
const current = {
  clones: statistics.clones,
  duplicatedLines: statistics.duplicatedLines,
  percentage: Number(statistics.percentage.toFixed(2)),
};
if (process.argv.includes("--baseline")) {
  writeFileSync(baselinePath, `${JSON.stringify(current, null, 2)}\n`);
  console.log(`Wrote ${baselinePath}: ${current.clones} clones / ${current.duplicatedLines} lines`);
  process.exit(0);
}
const baseline = JSON.parse(readFileSync(baselinePath, "utf8"));
let previous = null;
try {
  previous = readPreviousBaseline(repoRoot, "scripts/baselines/dry.json");
} catch (error) {
  console.error(error.message);
  process.exit(2);
}
if (previous && (baseline.clones > previous.clones || baseline.duplicatedLines > previous.duplicatedLines || baseline.percentage > previous.percentage)) {
  console.error("TypeScript DRY baseline expanded");
  process.exit(1);
}
const regressions = [];
for (const key of ["clones", "duplicatedLines", "percentage"]) {
  if (current[key] > baseline[key]) regressions.push(`${key}: ${current[key]} > ${baseline[key]}`);
}
const stale = [];
for (const key of ["clones", "duplicatedLines", "percentage"]) {
  if (current[key] < baseline[key]) stale.push(`${key}: ${current[key]} < ${baseline[key]}; lower the baseline`);
}
if (regressions.length || stale.length) {
  console.error("TypeScript DRY ratchet violation");
  for (const item of regressions) console.error(`  new debt: ${item}`);
  for (const item of stale) console.error(`  stale baseline: ${item}`);
  console.error("Extract the duplicated logic; lower the baseline only after the improvement is verified.");
  process.exit(1);
}
console.log(`TypeScript DRY ratchet OK (${current.clones} clones / ${current.duplicatedLines} lines / ${current.percentage}%)`);
