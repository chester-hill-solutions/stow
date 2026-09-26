import assert from "node:assert/strict";
import { readFileSync, statSync } from "node:fs";
import { resolve } from "node:path";
import { test } from "node:test";

const repoRoot = resolve(import.meta.dirname, "..");
const config = JSON.parse(readFileSync(resolve(repoRoot, "config/scan-roots.json"), "utf8"));

// The Go quality ratchet and the file-size ratchet each had their own list of what
// to scan, and they disagreed. tools/ and scripts/ were in neither, so the code
// that checks the code was itself unchecked - a gate with a list nobody
// cross-checks is a list that drifts, and this repository has now hit that shape
// seven separate times.
//
// These cases exist so the shared list cannot quietly lose a directory again.

// The two that were missed. A regression here is silent: nothing else in the
// repository looks at whether the gates are looking at the right thing.
//
// tools/ belongs to both because it holds Go. scripts/ belongs to the size list
// only, because it holds no Go at all and the quality ratchet reads .go files —
// listing it there would be a path that resolves and finds nothing, which looks
// like coverage and is not.
test("the shared list covers tools/ and scripts/", () => {
  assert.ok(config.go.includes("tools"), "go roots must include tools/");
  assert.ok(config.size.includes("tools"), "size roots must include tools/");
  assert.ok(config.size.includes("scripts"), "size roots must include scripts/");
});

// The Go ratchet is what checks Go, so a Go source directory it skips is Go that
// compiles without a reviewer noticing complexity.
test("the go list covers every Go source root in the repository", () => {
  for (const root of ["cmd", "internal", "conformance", "pkg", "tools"]) {
    assert.ok(config.go.includes(root), `go roots must include ${root}/`);
  }
});

// The size list also covers TypeScript and Python, and the client packages are
// where a generated-looking file tends to be large.
test("the size list covers the client packages", () => {
  for (const root of [
    "wasm",
    "scripts",
    "packages/stow-s3/src",
    "packages/stow-s3/test",
    "packages/stow-s3-py/src",
    "packages/stow-s3-py/tests",
  ]) {
    assert.ok(config.size.includes(root), `size roots must include ${root}`);
  }
});

// Every root the list names has to exist, or a gate is carrying a path that
// resolves to nothing and quietly checking less than it appears to.
test("every root the list names exists", () => {
  for (const key of ["go", "size"]) {
    for (const root of config[key]) {
      const stat = statSync(resolve(repoRoot, root), { throwIfNoEntry: false });
      assert.ok(stat?.isDirectory(), `${key} root ${root} is not a directory`);
    }
  }
});
