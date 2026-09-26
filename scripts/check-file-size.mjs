#!/usr/bin/env node
import { readFileSync, readdirSync, statSync, writeFileSync } from "node:fs";
import { join, relative, resolve } from "node:path";
import { readPreviousBaseline } from "./baseline-history.mjs";
import { compareIdentities, expandedKeys } from "./ratchet.mjs";

const repoRoot = resolve(import.meta.dirname, "..");
const baselinePath = resolve(repoRoot, "scripts/baselines/file-size.json");

// The root list is shared with the Go quality ratchet through
// config/scan-roots.json. The two each had their own and they disagreed:
// tools/ and scripts/ were in neither, so the code that checks the code was
// itself unchecked, and a gate with a list nobody cross-checks is a list that
// drifts.
const roots = JSON.parse(readFileSync(resolve(repoRoot, "config/scan-roots.json"), "utf8")).size;
const maximum = 500;
const violations = [];

function visit(path) {
  const stat = statSync(path);
  if (stat.isDirectory()) {
    if (["node_modules", "dist", "coverage"].includes(path.split("/").at(-1))) return;
    for (const entry of readdirSync(path)) visit(join(path, entry));
    return;
  }
  if (!/\.(go|ts|mjs|py)$/.test(path) || path.endsWith(".d.ts")) return;
  const lines = readFileSync(path, "utf8").split("\n").length;
  if (lines > maximum) violations.push({ identity: `${relative(repoRoot, path)}:${lines}`, lines });
}
for (const root of roots) {
  const path = resolve(repoRoot, root);
  try { if (statSync(path).isDirectory()) visit(path); } catch { /* missing optional root */ }
}
violations.sort((a, b) => a.identity.localeCompare(b.identity));

if (process.argv.includes("--baseline")) {
  writeFileSync(baselinePath, `${JSON.stringify({ maximum, violations }, null, 2)}\n`);
  console.log(`Wrote ${baselinePath}: ${violations.length} oversized files`);
  process.exit(0);
}
const baseline = JSON.parse(readFileSync(baselinePath, "utf8"));
let previous = null;
try {
  previous = readPreviousBaseline(repoRoot, "scripts/baselines/file-size.json");
} catch (error) {
  console.error(error.message);
  process.exit(2);
}
const expanded = expandedKeys(
  { maximum: baseline.maximum, entries: (baseline.violations ?? []).length },
  previous ? { maximum: previous.maximum, entries: (previous.violations ?? []).length } : null,
  ["maximum", "entries"],
);
if (expanded.length) {
  console.error(`File-size baseline expanded: ${expanded.join(", ")}`);
  process.exit(1);
}
const { added, stale } = compareIdentities(
  violations.map((item) => item.identity),
  (baseline.violations ?? []).map((item) => item.identity),
);
if (added.length || stale.length) {
  console.error("File-size ratchet violation");
  for (const item of added) console.error(`  new oversized file: ${item}`);
  for (const item of stale) console.error(`  stale baseline entry: ${item}`);
  process.exit(1);
}
console.log(`File-size ratchet OK (${violations.length} baseline entries; maximum ${maximum} lines)`);
