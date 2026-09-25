#!/usr/bin/env node
// Go coverage ratchet.
//
// The invariant is the number of covered statements, not the percentage.
// A percentage falls whenever well-tested code is added with defensive error
// branches that only a real failure would reach, which is how adding a 20
// statement system-call wrapper with 12 covered could drop the percentage by
// 0.04 points while coverage actually improved. Covered statements express
// what regression means here: fewer statements are being exercised.
//
// The percentage is still reported, and a large percentage drop is still a
// failure, so the statement count cannot be gamed by deleting a few heavily
// covered tests while adding a large volume of trivially covered code.
//
// The recorded numbers are a floor, not a target. Concurrency tests in
// internal/runthrough interleave differently between runs, so the measurement
// moves by a statement or two; recording the floor keeps a noisy run from
// failing while still catching a real loss of covered statements.
//
// Coverage is computed from the profile rather than read from
// "go tool cover -func", whose total is rounded to one decimal. A true value on
// a rounding boundary, such as 60.15%, otherwise reports as 60.1 on one run and
// 60.2 on the next, so the same commit passes and fails.
import { execFileSync } from "node:child_process";
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { resolve } from "node:path";
import { readPreviousBaseline } from "./baseline-history.mjs";

// A percentage drop larger than this is treated as a regression even if the
// covered statement count held, which is the case where statement counts alone
// would be gameable.
const MAX_PERCENTAGE_DROP = 1;

const repoRoot = resolve(import.meta.dirname, "..");
const baselinePath = resolve(repoRoot, "scripts/baselines/go-coverage.json");
const profilePath = resolve(repoRoot, ".cache/go-coverage.out");
mkdirSync(resolve(repoRoot, ".cache"), { recursive: true });
execFileSync("go", ["test", "./...", "-coverprofile", profilePath], { cwd: repoRoot, stdio: "inherit" });

const measured = coverageFromProfile(readFileSync(profilePath, "utf8"));

if (process.argv.includes("--baseline")) {
  writeFileSync(baselinePath, `${JSON.stringify(measured, null, 2)}\n`);
  console.log(
    `Wrote ${baselinePath}: ${measured.percentage}% (${measured.coveredStatements}/${measured.totalStatements} statements)`,
  );
  process.exit(0);
}
if (!existsSync(baselinePath)) {
  console.error(`Coverage baseline missing: ${baselinePath}`);
  process.exit(2);
}
const baseline = JSON.parse(readFileSync(baselinePath, "utf8"));
if (typeof baseline.coveredStatements !== "number" || typeof baseline.percentage !== "number") {
  console.error(
    "Coverage baseline must record coveredStatements and percentage; rewrite it with --baseline",
  );
  process.exit(2);
}

const problems = [];
if (measured.coveredStatements < baseline.coveredStatements) {
  problems.push(
    `covered statements fell: ${measured.coveredStatements} < ${baseline.coveredStatements}`,
  );
}
const drop = round(baseline.percentage - measured.percentage);
if (drop > MAX_PERCENTAGE_DROP) {
  problems.push(
    `coverage percentage fell by ${drop} points (${baseline.percentage}% -> ${measured.percentage}%), above the ${MAX_PERCENTAGE_DROP} point allowance`,
  );
}
// An improvement is reported, never a failure. The measurement varies by a
// statement or two between runs because concurrency tests in internal/runthrough
// interleave differently, so failing on an improvement would make the gate flap
// on scheduling rather than on coverage. Record a real improvement deliberately
// with --baseline, which is where the judgement belongs.

let previous = null;
try {
  previous = readPreviousBaseline(repoRoot, "scripts/baselines/go-coverage.json");
} catch (error) {
  console.error(error.message);
  process.exit(2);
}
if (previous && baseline.coveredStatements < previous.coveredStatements) {
  problems.push(
    `the stored baseline was lowered: ${baseline.coveredStatements} < ${previous.coveredStatements}`,
  );
}

if (problems.length > 0) {
  for (const problem of problems) {
    console.error(`Go coverage ratchet: ${problem}`);
  }
  process.exit(1);
}
const summary = `${measured.percentage}%, ${measured.coveredStatements}/${measured.totalStatements} statements`;
if (measured.coveredStatements > baseline.coveredStatements) {
  console.log(
    `Go coverage ratchet OK (${summary}); above the recorded floor of ${baseline.coveredStatements}, raise it with --baseline if the improvement is real`,
  );
} else {
  console.log(`Go coverage ratchet OK (${summary})`);
}

function coverageFromProfile(profile) {
  // Each line is "name.go:startLine.startCol,endLine.endCol numStatements count".
  const entry = /:\d+\.\d+,\d+\.\d+ (\d+) (\d+)$/;
  let totalStatements = 0;
  let coveredStatements = 0;
  for (const line of profile.split("\n")) {
    if (line.length === 0 || line.startsWith("mode:")) {
      continue;
    }
    const match = entry.exec(line);
    if (match === null) {
      continue;
    }
    const statements = Number(match[1]);
    const count = Number(match[2]);
    if (!Number.isFinite(statements) || !Number.isFinite(count)) {
      continue;
    }
    totalStatements += statements;
    if (count > 0) {
      coveredStatements += statements;
    }
  }
  if (totalStatements === 0) {
    console.error("Could not read statements from the Go coverage profile");
    process.exit(2);
  }
  return {
    percentage: round((coveredStatements / totalStatements) * 100),
    coveredStatements,
    totalStatements,
  };
}

function round(value) {
  return Math.round(value * 100) / 100;
}
