#!/usr/bin/env node
import { readFileSync, readdirSync, statSync, writeFileSync } from "node:fs";
import { createRequire } from "node:module";
import { join, relative, resolve } from "node:path";
import { readPreviousBaseline } from "./baseline-history.mjs";

const require = createRequire(import.meta.url);
const ts = require("../packages/stow-s3/node_modules/typescript");
const repoRoot = resolve(import.meta.dirname, "..");
const packageRoot = resolve(repoRoot, "packages/stow-s3");
const baselinePath = resolve(repoRoot, "scripts/baselines/type-escapes.json");
const violations = [];
const forbidden = [];
const expectErrorsWithoutDescription = [];

function position(sourceFile, offset) {
  const point = sourceFile.getLineAndCharacterOfPosition(offset);
  return `${point.line + 1}:${point.character + 1}`;
}

function isUnknownCastExpression(expression) {
  if (ts.isAsExpression(expression)) {
    return expression.type.kind === ts.SyntaxKind.UnknownKeyword || isUnknownCastExpression(expression.expression);
  }
  if (ts.isParenthesizedExpression(expression) || ts.isTypeAssertionExpression(expression)) {
    return isUnknownCastExpression(expression.expression);
  }
  return false;
}

function scan(path) {
  const source = readFileSync(path, "utf8");
  const sourceFile = ts.createSourceFile(path, source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  const add = (kind, node) => {
    violations.push(`${relative(repoRoot, path)}:${position(sourceFile, node.getStart(sourceFile))}:${kind}`);
  };
  const visit = (node) => {
    if (node.kind === ts.SyntaxKind.AnyKeyword) add("explicit-any", node);
    if (ts.isAsExpression(node)) {
      if (node.type.kind === ts.SyntaxKind.AnyKeyword) add("as-any", node);
      if (isUnknownCastExpression(node.expression)) add("double-cast", node);
    }
    if (ts.isTypeReferenceNode(node) && node.typeName.getText(sourceFile) === "any") {
      add("explicit-any", node);
    }
    ts.forEachChild(node, visit);
  };
  visit(sourceFile);

  const scanner = ts.createScanner(ts.ScriptTarget.Latest, false, ts.LanguageVariant.Standard, source);
  for (let token = scanner.scan(); token !== ts.SyntaxKind.EndOfFileToken; token = scanner.scan()) {
    if (token !== ts.SyntaxKind.SingleLineCommentTrivia && token !== ts.SyntaxKind.MultiLineCommentTrivia) continue;
    const comment = scanner.getTokenText();
    const start = scanner.getTokenPos();
    const point = sourceFile.getLineAndCharacterOfPosition(start);
    const location = `${relative(repoRoot, path)}:${point.line + 1}`;
    if (/@ts-(ignore|nocheck)\b/.test(comment)) forbidden.push(`${location}: suppression`);
    if (/@ts-expect-error\b/.test(comment)) {
      const textAfterDirective = comment.replace(/.*@ts-expect-error\b/, "").trim();
      if (!textAfterDirective) expectErrorsWithoutDescription.push(`${location}: expect-error-description`);
      else violations.push(`${location}:${point.character + 1}:expect-error`);
    }
  }
}

function visit(directory) {
  for (const entry of readdirSync(directory, { withFileTypes: true })) {
    const path = join(directory, entry.name);
    if (entry.name === "node_modules" || entry.name === "dist") continue;
    if (entry.isDirectory()) visit(path);
    else if (/\.tsx?$/.test(entry.name) && !entry.name.endsWith(".d.ts")) scan(path);
  }
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
  previous = readPreviousBaseline(repoRoot, "scripts/baselines/type-escapes.json");
} catch (error) {
  console.error(error.message);
  process.exit(2);
}
if (previous && (baseline.violations ?? []).length > (previous.violations ?? []).length) {
  console.error("TypeScript escape baseline increased");
  process.exit(1);
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
