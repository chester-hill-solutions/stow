import assert from "node:assert/strict";
import { test } from "node:test";

import { compareIdentities, compareKeys, expandedKeys } from "./ratchet.mjs";

// The ratchet policy, tested once for the four gates that use it. Each of those
// gates had this written out by hand and none of them had a test, so the policy
// that every gate depends on was only ever exercised by whatever tree happened to
// be checked out.

test("an unchanged set of keys passes", () => {
  const result = compareKeys({ complexity: 2, "max-depth": 0 }, { complexity: 2, "max-depth": 0 }, [
    "complexity",
    "max-depth",
  ]);
  assert.deepEqual(result.regressions, []);
  assert.deepEqual(result.stale, []);
});

test("new debt is a regression", () => {
  const result = compareKeys({ complexity: 3 }, { complexity: 2 }, ["complexity"]);
  assert.deepEqual(result.regressions, ["complexity: 3 > 2"]);
  assert.deepEqual(result.stale, []);
});

test("an improvement is stale until the baseline is lowered", () => {
  const result = compareKeys({ clones: 1 }, { clones: 4 }, ["clones"]);
  assert.deepEqual(result.regressions, []);
  assert.deepEqual(result.stale, ["clones: 1 < 4; lower the baseline"]);
});

test("a missing key on either side counts as zero", () => {
  const result = compareKeys({ complexity: 1 }, {}, ["complexity", "max-depth"]);
  assert.deepEqual(result.regressions, ["complexity: 1 > 0"]);
  assert.deepEqual(result.stale, []);
});

test("both directions are reported together", () => {
  const result = compareKeys({ a: 2, b: 0 }, { a: 1, b: 1 }, ["a", "b"]);
  assert.equal(result.regressions.length, 1);
  assert.equal(result.stale.length, 1);
  assert.match(result.regressions[0], /^a: 2 > 1$/);
  assert.match(result.stale[0], /^b: 0 < 1/);
});

test("a baseline that grew against history is caught", () => {
  const keys = ["complexity", "any"];
  const expanded = expandedKeys({ complexity: 3, any: 1 }, { complexity: 2, any: 1 }, keys);
  assert.deepEqual(expanded, ["complexity"]);
});

test("a baseline that shrank against history is fine", () => {
  const keys = ["complexity"];
  assert.deepEqual(expandedKeys({ complexity: 1 }, { complexity: 3 }, keys), []);
});

test("no previous baseline means nothing expanded", () => {
  assert.deepEqual(expandedKeys({ complexity: 99 }, null, ["complexity"]), []);
});

test("a new metric counts as an increase from zero", () => {
  const expanded = expandedKeys({ complexity: 1, escape: 2 }, { complexity: 1 }, ["complexity", "escape"]);
  assert.deepEqual(expanded, ["escape"]);
});

// The check that stops a gate being satisfied by raising the baseline to match
// new debt: current equals baseline, so the per-key comparison is satisfied, and
// only the comparison against history notices that the floor moved.
test("a baseline raised to match new debt is caught by the history check", () => {
  const current = { complexity: 4 };
  const baseline = { complexity: 4 };
  const previous = { complexity: 1 };
  assert.deepEqual(compareKeys(current, baseline, ["complexity"]), { regressions: [], stale: [] });
  assert.deepEqual(expandedKeys(baseline, previous, ["complexity"]), ["complexity"]);
});

test("identical identity sets pass", () => {
  const result = compareIdentities(["a", "b"], ["a", "b"]);
  assert.deepEqual(result.added, []);
  assert.deepEqual(result.stale, []);
});

test("a new identity is unapproved debt", () => {
  const result = compareIdentities(["a", "newcomer"], ["a"]);
  assert.deepEqual(result.added, ["newcomer"]);
  assert.deepEqual(result.stale, []);
});

test("a stale identity is debt that was paid and not recorded", () => {
  const result = compareIdentities(["a"], ["a", "paid"]);
  assert.deepEqual(result.added, []);
  assert.deepEqual(result.stale, ["paid"]);
});

test("a renamed identity is caught even though the count is unchanged", () => {
  const result = compareIdentities(["b.go:after"], ["b.go:before"]);
  assert.deepEqual(result.added, ["b.go:after"]);
  assert.deepEqual(result.stale, ["b.go:before"]);
});

// A repeated identity must not read as two violations: a caller that compares
// lengths would otherwise see growth where there is one problem, and the report
// would name the same file twice for one fault.
test("a repeated current identity is reported once", () => {
  const result = compareIdentities(["a", "a", "b"], []);
  assert.deepEqual(result.added, ["a", "b"]);
  assert.deepEqual(result.stale, []);
});

test("empty baselines behave", () => {
  assert.deepEqual(compareIdentities([], []), { added: [], stale: [] });
  assert.deepEqual(compareKeys({}, {}, ["a"]), { regressions: [], stale: [] });
});
