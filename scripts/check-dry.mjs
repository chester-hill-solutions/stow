#!/usr/bin/env node
import { execFileSync } from "node:child_process";
import { existsSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { resolve } from "node:path";
import { readPreviousBaseline } from "./baseline-history.mjs";
import { compareKeys, expandedKeys } from "./ratchet.mjs";

const repoRoot = resolve(import.meta.dirname, "..");
const packageRoot = resolve(repoRoot, "packages/stow-s3");
const baselinePath = resolve(repoRoot, "scripts/baselines/dry.json");
const reportPath = resolve(packageRoot, "node_modules/.cache/jscpd/jscpd-report.json");

// The previous run's report is removed first. Otherwise a jscpd that cannot run
// at all leaves the last good report in place, this gate reads it, and the
// ratchet passes on numbers that describe code nobody is looking at any more.
// A DRY gate that silently reports a clean bill of health for a tree it never
// scanned is worse than one that fails.
rmSync(reportPath, { force: true });

// jscpd's output is captured rather than discarded, because the most common
// failure here is not clones at all: jscpd resolves its scanner through a
// platform-specific optional dependency, and when that package is absent it
// prints a one-line explanation, writes no report, and *exits 0*. Discarding its
// output turned that into "DRY report not found", which points at the report
// path and says nothing about the cause.
let jscpdOutput = "";
try {
  jscpdOutput = execFileSync(resolve(packageRoot, "node_modules/.bin/jscpd"), [], {
    cwd: packageRoot,
    encoding: "utf8",
    stdio: ["ignore", "pipe", "pipe"],
  });
} catch (error) {
  // A non-zero exit still produces a usable report, so this is not fatal on its
  // own. The captured output is what makes the failure below diagnosable.
  jscpdOutput = `${error.stdout ?? ""}${error.stderr ?? ""}`;
}
if (!existsSync(reportPath)) {
  console.error(`DRY report not found: ${reportPath}`);
  if (jscpdOutput.trim()) {
    console.error(`jscpd reported:\n${jscpdOutput.trim()}`);
  }
  console.error(
    "jscpd resolves its scanner through a platform-specific optional dependency. " +
      "If the message above mentions one, install optional dependencies: npm install --include=optional",
  );
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
const KEYS = ["clones", "duplicatedLines", "percentage"];
const expanded = expandedKeys(baseline, previous, KEYS);
if (expanded.length) {
  console.error(`TypeScript DRY baseline expanded: ${expanded.join(", ")}`);
  process.exit(1);
}
const { regressions, stale } = compareKeys(current, baseline, KEYS);
if (regressions.length || stale.length) {
  console.error("TypeScript DRY ratchet violation");
  for (const item of regressions) console.error(`  new debt: ${item}`);
  for (const item of stale) console.error(`  stale baseline: ${item}`);
  console.error("Extract the duplicated logic; lower the baseline only after the improvement is verified.");
  process.exit(1);
}
console.log(`TypeScript DRY ratchet OK (${current.clones} clones / ${current.duplicatedLines} lines / ${current.percentage}%)`);
