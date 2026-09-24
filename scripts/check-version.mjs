#!/usr/bin/env node
import { readFileSync } from "node:fs";
import { resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const goSource = readFileSync(resolve(root, "internal/version/version.go"), "utf8");
const goVersion = goSource.match(/const Version = "([^"]+)"/)?.[1];
const packageJson = JSON.parse(readFileSync(resolve(root, "packages/stow/package.json"), "utf8"));
if (!goVersion || goVersion !== packageJson.version) {
  console.error(`Version mismatch: internal/version=${goVersion ?? "missing"}, package=${packageJson.version}`);
  process.exit(1);
}
console.log(`Version source OK (${goVersion})`);
