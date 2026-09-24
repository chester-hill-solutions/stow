#!/usr/bin/env node
import { execFileSync } from "node:child_process";
import { readFileSync, readdirSync, statSync, writeFileSync } from "node:fs";
import { join, relative, resolve } from "node:path";

const repoRoot = resolve(import.meta.dirname, "..");
const packageRoot = resolve(repoRoot, "packages/stow");
const baselinePath = resolve(repoRoot, "scripts/baselines/type-escapes.json");
const violations = [];
const forbidden = [];
const expectErrorsWithoutDescription = [];

function visit(directory) {
  for (const entry of readdirSync(directory, { withFileTypes: true })) {
    const path = join(directory, entry.name);
    if (entry.name === "node_modules" || entry.name === "dist") continue;
    if (entry.isDirectory()) visit(path);
    else if (/\.tsx?$/.test(entry.name) && !entry.name.endsWith(".d.ts")) scan(path);
  }
}

function scan(path) {
  const lines = readFileSync(path, "utf8").split("\n");
  lines.forEach((line, index) => {
    const location = `${relative(repoRoot, path)}:${index + 1}`;
    if (/@ts-(ignore|nocheck)\b/.test(line)) forbidden.push(`${location}: suppression`);
    if (/@ts-expect-error\b(?:\s*$|\s*\*\/\s*$)/.test(line)) expectErrorsWithoutDescription.push(`${location}: expect-error-description`);
    for (const [kind, pattern] of [
      ["as-any", /\bas\s+any\b/g],
      ["double-cast", /\bas\s+unknown\s+as\b/g],
      ["explicit-any", /\bany\b/g],
    ]) {
      for (const match of line.matchAll(pattern)) violations.push(`${location}:${match.index + 1}:${kind}`);
    }
  });
}

for (const root of ["src", "test"]) {
  const path = join(packageRoot, root);
  if (statSync(path, { throwIfNoEntry: false })) visit(path);
}

if (process.argv.includes("--baseline")) {
  writeFileSync(baselinePath, `${JSON.stringify({ violations: violations.sort() }, null, 2)}\n`);
  console.log(`Wrote ${baselinePath}`);
  process.exit(0);
}

const baseline = JSON.parse(readFileSync(baselinePath, "utf8"));
const allowed = new Set(baseline.violations ?? []);
const newViolations = violations.filter((item) => !allowed.has(item));
const staleEntries = [...allowed].filter((item) => !violations.includes(item));
let previous = null;
try {
  previous = JSON.parse(execFileSync("git", ["show", "HEAD:scripts/baselines/type-escapes.json"], { cwd: repoRoot, encoding: "utf8", stdio: ["ignore", "pipe", "ignore"] }));
} catch {
  // First baseline creation has no parent version.
}
if (previous) {
  const prior = new Set(previous.violations ?? []);
  if ((baseline.violations ?? []).length > prior.size) {
    console.error("TypeScript escape baseline increased");
    process.exit(1);
  }
}
if (newViolations.length || staleEntries.length || forbidden.length || expectErrorsWithoutDescription.length) {
  console.error("TypeScript escape ratchet violation");
  for (const item of newViolations) console.error(`  new: ${item}`);
  for (const item of staleEntries) console.error(`  stale: ${item}`);
  for (const item of forbidden) console.error(`  forbidden: ${item}`);
  for (const item of expectErrorsWithoutDescription) console.error(`  ${item}`);
  console.error("Remove the escape; never add a baseline entry to hide a regression.");
  process.exit(1);
}
console.log(`TypeScript escape ratchet OK (${violations.length} baseline entries)`);
