#!/usr/bin/env node
import { execFileSync } from "node:child_process";
import { readFileSync, writeFileSync } from "node:fs";
import { dirname, join, relative, resolve } from "node:path";
import { readPreviousBaseline } from "./baseline-history.mjs";
import { compareKeys, expandedKeys } from "./ratchet.mjs";

const repoRoot = resolve(import.meta.dirname, "..");
const packageRoot = resolve(repoRoot, "packages/stow-s3");
const baselinePath = resolve(repoRoot, "scripts/baselines/lint-ratchet.json");
const rules = [
  "complexity",
  "max-depth",
  "max-params",
  "max-lines-per-function",
  "no-console",
  "no-return-await",
  "@typescript-eslint/no-explicit-any",
  "@typescript-eslint/no-non-null-assertion",
  "@typescript-eslint/consistent-type-imports",
  "@typescript-eslint/no-unused-vars",
];

const output = execFileSync(
  process.execPath,
  [resolve(packageRoot, "node_modules/eslint/bin/eslint.js"), ".", "--format", "json", "--no-cache"],
  { cwd: packageRoot, encoding: "utf8", maxBuffer: 64 * 1024 * 1024 },
);
const reports = JSON.parse(output || "[]");
const counts = Object.fromEntries(rules.map((rule) => [rule, 0]));
const hardErrors = [];

// Suppressions are counted from ESLint's own report rather than by matching
// `eslint-disable` text in the source.
//
// The regex this replaces could not see a multi-line disable block, and matched
// any line merely mentioning a ratcheted rule name beside the word eslint-disable
// - including inside a string or an unrelated comment. It also cost one count per
// comment however many violations the comment hid, so a single directive could
// suppress an unlimited number of findings for the price of one.
//
// report.suppressedMessages is ESLint's own accounting: one entry per suppressed
// message, carrying the rule that would have fired. A disable hiding five calls
// costs five now, and a directive the linter never applied costs nothing.
for (const report of reports) {
  for (const message of report.messages ?? []) {
    if (message.severity === 2) {
      hardErrors.push(`${relative(packageRoot, report.filePath)}:${message.line} [${message.ruleId}] ${message.message}`);
    } else if (message.ruleId in counts) {
      counts[message.ruleId] += 1;
    }
  }
  for (const message of report.suppressedMessages ?? []) {
    if (message.ruleId in counts) counts[message.ruleId] += 1;
  }
}

if (process.argv.includes("--baseline")) {
  writeFileSync(baselinePath, `${JSON.stringify({ _comment: "Ratchet baseline for the TypeScript wrapper. Growth fails; lower intentionally after debt reduction.", counts }, null, 2)}\n`);
  console.log(`Wrote ${baselinePath}`);
  process.exit(0);
}

if (hardErrors.length > 0) {
  console.error("TypeScript lint hard errors:");
  for (const error of hardErrors) console.error(`  ${error}`);
  process.exit(1);
}

const baseline = JSON.parse(readFileSync(baselinePath, "utf8"));
let previous = null;
try {
  previous = readPreviousBaseline(repoRoot, "scripts/baselines/lint-ratchet.json");
} catch (error) {
  console.error(error.message);
  process.exit(2);
}
const expanded = expandedKeys(baseline.counts ?? {}, previous?.counts, rules);
if (expanded.length) {
  console.error(`TypeScript lint baseline expanded: ${expanded.join(", ")}`);
  process.exit(1);
}
const { regressions, stale } = compareKeys(counts, baseline.counts ?? {}, rules);
if (regressions.length || stale.length) {
  console.error("TypeScript lint ratchet violation");
  for (const item of regressions) console.error(`  new debt: ${item}`);
  for (const item of stale) console.error(`  stale baseline: ${item}`);
  console.error("Fix the violation; do not raise the baseline to pass.");
  process.exit(1);
}
console.log(`TypeScript lint ratchet OK (${rules.map((rule) => `${rule}=${counts[rule]}`).join(", ")})`);
