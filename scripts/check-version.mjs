#!/usr/bin/env node
import { execFileSync } from "node:child_process";
import { existsSync, readFileSync } from "node:fs";
import { resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const goSource = readFileSync(resolve(root, "internal/version/version.go"), "utf8");
const goVersion = goSource.match(/const Version = "([^"]+)"/)?.[1];
const packageJson = JSON.parse(readFileSync(resolve(root, "packages/stow/package.json"), "utf8"));
const problems = [];
if (!goVersion || goVersion !== packageJson.version) {
  problems.push(`version mismatch: internal/version=${goVersion ?? "missing"}, packages/stow=${packageJson.version}`);
}

// The platform packages carry the native binary, so a version skew between them
// and the main package would publish a tarball whose binary is from a different
// release than the JavaScript that resolves it.
const PLATFORM_PACKAGES = [
  { dir: "stow-linux-x64", name: "@chs/stow-linux-x64", os: "linux", cpu: "x64" },
  { dir: "stow-linux-arm64", name: "@chs/stow-linux-arm64", os: "linux", cpu: "arm64" },
  { dir: "stow-darwin-x64", name: "@chs/stow-darwin-x64", os: "darwin", cpu: "x64" },
  { dir: "stow-darwin-arm64", name: "@chs/stow-darwin-arm64", os: "darwin", cpu: "arm64" },
];

const optional = packageJson.optionalDependencies ?? {};
for (const platform of PLATFORM_PACKAGES) {
  const manifestPath = resolve(root, "packages", platform.dir, "package.json");
  if (!existsSync(manifestPath)) {
    problems.push(`missing platform package manifest: packages/${platform.dir}/package.json`);
    continue;
  }
  // A manifest that exists in the working tree but is untracked would pass every
  // other check here and still break a fresh clone, which is how these packages
  // were once left out of a commit while the local gate stayed green.
  try {
    execFileSync("git", ["ls-files", "--error-unmatch", `packages/${platform.dir}/package.json`], {
      cwd: root,
      stdio: "ignore",
    });
  } catch {
    problems.push(`untracked platform package manifest: packages/${platform.dir}/package.json`);
    continue;
  }
  const manifest = JSON.parse(readFileSync(manifestPath, "utf8"));
  if (manifest.name !== platform.name) {
    problems.push(`packages/${platform.dir} is named ${manifest.name}, want ${platform.name}`);
  }
  if (manifest.version !== goVersion) {
    problems.push(`packages/${platform.dir} is version ${manifest.version}, want ${goVersion}`);
  }
  if (JSON.stringify(manifest.os) !== JSON.stringify([platform.os])) {
    problems.push(`packages/${platform.dir} declares os ${JSON.stringify(manifest.os)}, want ["${platform.os}"]`);
  }
  if (JSON.stringify(manifest.cpu) !== JSON.stringify([platform.cpu])) {
    problems.push(`packages/${platform.dir} declares cpu ${JSON.stringify(manifest.cpu)}, want ["${platform.cpu}"]`);
  }
  if (optional[platform.name] !== goVersion) {
    problems.push(`optionalDependencies.${platform.name} is ${optional[platform.name] ?? "missing"}, want ${goVersion}`);
  }
}
const declared = Object.keys(optional).filter((name) => !PLATFORM_PACKAGES.some((p) => p.name === name));
if (declared.length > 0) {
  problems.push(`unexpected optionalDependencies: ${declared.join(", ")}`);
}

if (problems.length > 0) {
  for (const problem of problems) console.error(problem);
  process.exit(1);
}
console.log(`Version source OK (${goVersion}); ${PLATFORM_PACKAGES.length} platform packages in step`);
