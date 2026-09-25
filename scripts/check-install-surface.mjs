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
  const text = readFileSync(full, "utf8");

  for (const entry of SURFACE) {
    const mentioned = text.includes(entry.name) || text.includes(entry.command);
    // A doc that says a package is installable has to name it. A doc that is
    // honest about it not being published has to say so near the mention, so a
    // reader cannot see the install line without the caveat.
    const disclaimed = /not published|not yet|not on npm|not on PyPI|404/i.test(text);

    if (entry.published && !mentioned) {
      problems.push(`${doc} does not mention the published ${entry.ecosystem} target ${entry.name}`);
    }
    if (entry.published && !disclaimed) {
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
  const goModule = SURFACE.find((entry) => entry.ecosystem === "go").name;
  const repoPath = goModule.split("/pkg/")[0];
  for (const match of text.matchAll(/github\.com\/chester-hill-solutions\/[A-Za-z0-9._-]+/g)) {
    if (match[0] !== repoPath) {
      problems.push(
        `${doc} references ${match[0]} but the Go module lives at ${repoPath}; ` +
          "one of the two is stale and no published version of the other can resolve",
      );
    }
  }
}

if (process.argv.includes("--online")) {
  const checks = [
    ...SURFACE.filter((e) => e.ecosystem === "npm").map((e) => ({
      ...e,
      url: `https://registry.npmjs.org/${e.name.replace("/", "%2f")}`,
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
