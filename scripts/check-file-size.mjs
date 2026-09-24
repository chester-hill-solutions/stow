#!/usr/bin/env node
import { execFileSync } from "node:child_process";
import { readFileSync, readdirSync, statSync, writeFileSync } from "node:fs";
import { join, relative, resolve } from "node:path";

const repoRoot = resolve(import.meta.dirname, "..");
const baselinePath = resolve(repoRoot, "scripts/baselines/file-size.json");
const roots = ["cmd", "internal", "conformance", "packages/stow/src", "packages/stow/test"];
const maximum = 500;
const violations = [];

function visit(path) {
  const stat = statSync(path);
  if (stat.isDirectory()) {
    if (["node_modules", "dist", "coverage"].includes(path.split("/").at(-1))) return;
    for (const entry of readdirSync(path)) visit(join(path, entry));
    return;
  }
  if (!/\.(go|ts)$/.test(path) || path.endsWith(".d.ts")) return;
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
  previous = JSON.parse(execFileSync("git", ["show", "HEAD:scripts/baselines/file-size.json"], { cwd: repoRoot, encoding: "utf8", stdio: ["ignore", "pipe", "ignore"] }));
} catch {
  // First baseline creation has no parent version.
}
if (previous && (baseline.maximum > previous.maximum || (baseline.violations ?? []).length > (previous.violations ?? []).length)) {
  console.error("File-size baseline expanded");
  process.exit(1);
}
const actual = new Set(violations.map((item) => item.identity));
const allowed = new Set((baseline.violations ?? []).map((item) => item.identity));
const added = [...actual].filter((item) => !allowed.has(item));
const stale = [...allowed].filter((item) => !actual.has(item));
if (added.length || stale.length) {
  console.error("File-size ratchet violation");
  for (const item of added) console.error(`  new oversized file: ${item}`);
  for (const item of stale) console.error(`  stale baseline entry: ${item}`);
  process.exit(1);
}
console.log(`File-size ratchet OK (${violations.length} baseline entries; maximum ${maximum} lines)`);
