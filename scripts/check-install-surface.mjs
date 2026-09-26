#!/usr/bin/env node
// Checks that the documented install surface matches what actually exists.
//
// Issue #3 was three sets of install instructions, in the README and in the two
// files an agent is most likely to be handed first, pointing at an npm package,
// a PyPI package and a Go module path that did not exist. Nothing failed. The
// repository has a gate for almost every other way it can drift, and this was
// the one hole in it.
//
// The fix is to make publication status a declared fact in one place rather than
// something each document asserts in prose, and then check the documents against
// it. Publishing becomes flipping `published` here and updating the prose, and
// the gate fails until both are done. A document cannot quietly claim a package
// is installable when this file says it is not.
//
// The default run is offline and deterministic so it is safe in CI. Pass
// --online to additionally resolve each published target against its registry,
// which is the check that would have caught issue #3 on its own, but which
// depends on the network and so does not belong in a required check.
import { existsSync, readFileSync } from "node:fs";
import { resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");

// The single declared source of truth. `published` is a claim about the world,
// not about this repository, which is why it is the one thing here that cannot be
// derived from the tree and has to be maintained deliberately.
const SURFACE = [
  {
    ecosystem: "npm",
    name: "@chester-hill-solutions/stow-s3",
    published: false,
    // The name a reader would put on a command line.
    command: "npm install @chester-hill-solutions/stow-s3",
    // The path in this repository that backs the package.
    path: "packages/stow-s3",
  },
  {
    ecosystem: "pypi",
    name: "stow-s3",
    published: false,
    command: 'pip install "stow-s3[boto3]"',
    path: "packages/stow-s3-py",
  },
  {
    ecosystem: "go",
    name: "github.com/chester-hill-solutions/stow-s3/pkg/stow",
    published: true,
    command: "go get github.com/chester-hill-solutions/stow-s3/pkg/stow",
    path: "pkg/stow",
  },
];

// Every file that tells a reader how to install. A new one has to be added here
// or the gate cannot see it, which is the intended pressure.
const DOCS = ["README.md", "site/agent.md", "site/llms.txt", "skills/stow-s3/SKILL.md"];

const DISCLAIMER = /not published|not yet|not on npm|not on PyPI|404/i;

// docProblems is the whole rule, as a pure function of a declaration and a
// document's text, so it can be tested in every mode it has.
//
// It is pure because the bug it used to have was only reachable in a mode
// nothing exercised. The gate demanded a disclaimer for a PUBLISHED target
// unconditionally, which was right while some target was still unpublished and
// became a demand for a false statement the moment all of them were published -
// the exact moment the rule was supposed to stop mattering. With npm and PyPI
// still unpublished that branch never ran, so the gate had never once been
// asked whether it would pass in the state it is meant to end in. A mode that
// is not exercised is not tested code, however green it is today.
export function docProblems(surface, doc, text) {
  const problems = [];
  const disclaimed = DISCLAIMER.test(text);
  // A disclaimer is owed only while something is actually unreachable. Once
  // every target is published there is nothing to warn about, and asking for
  // one anyway would force "not published" into four documents forever.
  const anyUnpublished = surface.some((entry) => !entry.published);

  for (const entry of surface) {
    const mentioned = text.includes(entry.name) || text.includes(entry.command);

    if (entry.published && !mentioned) {
      problems.push(`${doc} does not mention the published ${entry.ecosystem} target ${entry.name}`);
    }
    if (anyUnpublished && entry.published && !disclaimed) {
      problems.push(
        `${doc} mentions ${entry.name} but never says the other packages are unpublished; ` +
          "an agent reading it will try an install that 404s",
      );
    }
    if (!entry.published && !disclaimed) {
      problems.push(
        `${doc} does not say that ${entry.ecosystem} ${entry.name} is unpublished, ` +
          "so its install line reads as working",
      );
    }
  }

  // The Go module path is also the repository URL, and the two have to be the
  // same string. They diverged once already: the module was renamed to stow-s3
  // while the remote was still stow, so the documented `go get` could not
  // resolve and the documented repository URL did not exist.
  const goEntry = surface.find((entry) => entry.ecosystem === "go");
  if (goEntry) {
    const repoPath = goEntry.name.split("/pkg/")[0];
    for (const match of text.matchAll(/github\.com\/chester-hill-solutions\/[A-Za-z0-9._-]+/g)) {
      if (match[0] !== repoPath) {
        problems.push(
          `${doc} references ${match[0]} but the Go module lives at ${repoPath}; ` +
            "one of the two is stale and no published version of the other can resolve",
        );
      }
    }
  }
  return problems;
}

export { SURFACE, DOCS };

const problems = [];

for (const entry of SURFACE) {
  if (!existsSync(resolve(root, entry.path))) {
    problems.push(`${entry.ecosystem} ${entry.name} claims a repo path that does not exist: ${entry.path}`);
  }
}

for (const doc of DOCS) {
  const full = resolve(root, doc);
  if (!existsSync(full)) {
    problems.push(`install-surface doc missing: ${doc}`);
    continue;
  }
  problems.push(...docProblems(SURFACE, doc, readFileSync(full, "utf8")));
}

if (process.argv.includes("--online")) {
  const checks = [
    ...SURFACE.filter((e) => e.ecosystem === "npm").map((e) => ({
      ...e,
      // GitHub Packages, not npmjs.org. This organisation publishes there and
      // owns nothing on npmjs, where the scope does not exist at all, so
      // checking npmjs reported the package as unpublished no matter what had
      // been published.
      url: `https://npm.pkg.github.com/${e.name.replace("/", "%2f")}`,
    })),
    ...SURFACE.filter((e) => e.ecosystem === "pypi").map((e) => ({
      ...e,
      url: `https://pypi.org/pypi/${e.name}/json`,
    })),
    ...SURFACE.filter((e) => e.ecosystem === "go").map((e) => ({
      ...e,
      url: `https://proxy.golang.org/${e.name.split("/pkg/")[0]}/@latest`,
    })),
  ];
  for (const check of checks) {
    let reachable = false;
    let detail = "";
    try {
      const response = await fetch(check.url);
      reachable = response.ok;
      detail = `HTTP ${response.status}`;
    } catch (error) {
      detail = error.message;
    }
    if (reachable !== check.published) {
      problems.push(
        `${check.ecosystem} ${check.name} is ${reachable ? "reachable" : "not reachable"} (${detail}) ` +
          `but is declared published=${check.published}`,
      );
    } else {
      console.log(
        `  ${reachable ? "ok" : "unpublished"}  ${check.ecosystem} ${check.name} (${detail})`,
      );
    }
  }
}

if (problems.length > 0) {
  console.error("Install surface does not match reality:\n");
  for (const problem of problems) {
    console.error(`  - ${problem}`);
  }
  console.error(
    "\nIf a package was published, set published: true in scripts/check-install-surface.mjs " +
      "and update the prose in the files listed above. If it was not, the prose is wrong.",
  );
  process.exit(1);
}

console.log(
  `Install surface OK (${SURFACE.filter((e) => e.published).length}/${SURFACE.length} published, ` +
    `${DOCS.length} docs)` +
    (process.argv.includes("--online") ? "; registries checked" : ""),
);
