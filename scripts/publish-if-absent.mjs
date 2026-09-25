#!/usr/bin/env node
// Publish npm tarballs, skipping any whose exact version is already on the registry.
//
//   node scripts/publish-if-absent.mjs <tarball> [<tarball> ...]
//
// Two reasons this exists rather than a bare `npm publish`:
//
// `npm publish` takes one package spec. Publishing the four platform packages
// with `npm publish ./dist/npm-platform/*.tgz` hands npm four positional
// arguments and it exits EUSAGE without contacting the registry, which reads
// like a credential problem and is not one. Each tarball is published here, one
// at a time.
//
// A release has to be re-runnable. A tag is immutable once consumers can see
// it, but a publish is several independent writes, and any of them can fail
// after an earlier one has already landed. Re-running the whole release then
// fails on the packages that succeeded, which is how a single transient error
// turns into a version that can never be completed. Checking the registry
// first makes the second run publish only what is missing.
import { execFileSync } from "node:child_process";

const tarballs = process.argv.slice(2);
if (tarballs.length === 0) {
  console.error("usage: publish-if-absent.mjs <tarball> [<tarball> ...]");
  process.exit(64);
}

function manifestOf(tarball) {
  const raw = execFileSync("tar", ["-xzOf", tarball, "package/package.json"], {
    encoding: "utf8",
  });
  const manifest = JSON.parse(raw);
  if (!manifest.name || !manifest.version) {
    throw new Error(`${tarball} has no name or version in package/package.json`);
  }
  return manifest;
}

function isPublished(name, version) {
  // A non-zero exit is the signal, not the output: npm prints a 404 to stderr
  // and writes nothing to stdout for a version that does not exist.
  try {
    execFileSync("npm", ["view", `${name}@${version}`, "version"], {
      stdio: ["ignore", "ignore", "ignore"],
    });
    return true;
  } catch {
    return false;
  }
}

for (const tarball of tarballs) {
  const { name, version } = manifestOf(tarball);

  if (isPublished(name, version)) {
    console.log(`skip  ${name}@${version} (already on the registry)`);
    continue;
  }

  console.log(`publish ${name}@${version}`);
  execFileSync("npm", ["publish", "--ignore-scripts", "--access", "public", tarball], {
    stdio: "inherit",
  });
}
