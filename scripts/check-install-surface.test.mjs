import assert from "node:assert/strict";
import { test } from "node:test";

import { docProblems, SURFACE } from "./check-install-surface.mjs";

// A surface where everything is published. This is the state the repository is
// heading toward and the one the gate had never been asked about.
const ALL_PUBLISHED = SURFACE.map((entry) => ({ ...entry, published: true }));

// A surface where nothing is published.
const NONE_PUBLISHED = SURFACE.map((entry) => ({ ...entry, published: false }));

const goEntry = SURFACE.find((entry) => entry.ecosystem === "go");

function docFor(surface, { disclaimer }) {
  const lines = surface.map((entry) => `Install with: ${entry.command}`);
  if (disclaimer) lines.push("The npm and PyPI packages are not published yet.");
  return lines.join("\n");
}

test("a doc with no disclaimer fails while something is unpublished", () => {
  const problems = docProblems(SURFACE, "README.md", docFor(SURFACE, { disclaimer: false }));
  assert.ok(
    problems.some((p) => p.includes("is unpublished")),
    `expected a disclaimer problem, got ${JSON.stringify(problems)}`,
  );
});

test("a doc with a disclaimer passes while something is unpublished", () => {
  const problems = docProblems(SURFACE, "README.md", docFor(SURFACE, { disclaimer: true }));
  assert.deepEqual(problems, [], `expected no problems, got ${JSON.stringify(problems)}`);
});

// The regression. Once every target is published there is nothing to warn a
// reader about, so a doc that simply states the install commands is correct and
// complete. The rule used to demand a disclaimer regardless, which meant the
// post-publish flip would have forced a false "not published" into four
// documents, and the branch that could see it never ran because two targets
// were still unpublished.
test("once everything is published, no disclaimer is owed", () => {
  const problems = docProblems(
    ALL_PUBLISHED,
    "README.md",
    docFor(ALL_PUBLISHED, { disclaimer: false }),
  );
  assert.deepEqual(
    problems,
    [],
    `a fully published surface must not demand a disclaimer, got ${JSON.stringify(problems)}`,
  );
});

test("once everything is published, a disclaimer is not forbidden either", () => {
  const problems = docProblems(
    ALL_PUBLISHED,
    "README.md",
    docFor(ALL_PUBLISHED, { disclaimer: true }),
  );
  assert.deepEqual(problems, [], `got ${JSON.stringify(problems)}`);
});

test("an unpublished target must still be named as unpublished when nothing else is", () => {
  // One published, two not: the disclaimer is still owed, because a reader can
  // still try an install that 404s.
  const problems = docProblems(NONE_PUBLISHED, "README.md", docFor(NONE_PUBLISHED, { disclaimer: false }));
  assert.ok(
    problems.length >= 2,
    `expected one problem per unpublished target, got ${JSON.stringify(problems)}`,
  );
});

test("a published target that no doc mentions is a problem", () => {
  // The Go module is the one published target today, so a doc that never names
  // it is the case: a reader is told nothing about how to get it.
  const text = "The npm package is not published yet.";
  const problems = docProblems(SURFACE, "README.md", text);
  assert.ok(
    problems.some((p) => p.includes("does not mention the published")),
    `expected a missing-mention problem, got ${JSON.stringify(problems)}`,
  );
});

test("a stale repository reference is a problem in every mode", () => {
  for (const surface of [ALL_PUBLISHED, NONE_PUBLISHED]) {
    const problems = docProblems(
      surface,
      "README.md",
      `See https://github.com/chester-hill-solutions/stow for details. The npm package is not published yet.`,
    );
    assert.ok(
      problems.some((p) => p.includes("but the Go module lives at")),
      `expected a stale-repo problem, got ${JSON.stringify(problems)}`,
    );
  }
});

// The module path and the repository URL are the same string. They diverged
// once, when the module was renamed and the remote was not.
test("a doc may reference the module path as a repository URL", () => {
  const repo = goEntry.name.split("/pkg/")[0];
  const problems = docProblems(
    ALL_PUBLISHED,
    "README.md",
    `${docFor(ALL_PUBLISHED, { disclaimer: false })}\nSource: ${repo}`,
  );
  assert.deepEqual(problems, [], `got ${JSON.stringify(problems)}`);
});
