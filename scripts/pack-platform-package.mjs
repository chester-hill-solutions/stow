#!/usr/bin/env node
// Pack one native platform package from a release archive.
//
//   node scripts/pack-platform-package.mjs <archive.tar.gz> [out-dir]
//
// The archive is named stow-<goos>-<goarch>.tar.gz by the release build. The
// npm package name uses npm's arch spelling, which differs for amd64/x64, so
// the translation lives here rather than in the workflow: a mismatch would
// otherwise fail only at release time.
import { chmodSync, existsSync, mkdirSync, mkdtempSync, copyFileSync, readFileSync, rmSync } from "node:fs";
import { execFileSync } from "node:child_process";
import { tmpdir } from "node:os";
import { basename, join, resolve } from "node:path";

const NPM_ARCH = { amd64: "x64", arm64: "arm64" };

const root = resolve(import.meta.dirname, "..");
const [archiveArg, outDirArg] = process.argv.slice(2);
if (!archiveArg) {
  console.error("usage: pack-platform-package.mjs <archive.tar.gz> [out-dir]");
  process.exit(64);
}

const archive = resolve(archiveArg);
const outDir = resolve(root, outDirArg ?? "dist/npm-platform");
const match = basename(archive).match(/^stow-([a-z0-9]+)-([a-z0-9]+)\.tar\.gz$/);
if (!match) {
  console.error(`archive name is not stow-<goos>-<goarch>.tar.gz: ${basename(archive)}`);
  process.exit(1);
}
const [, goos, goarch] = match;
const npmArch = NPM_ARCH[goarch];
if (npmArch === undefined) {
  console.error(`no npm platform package defined for ${goos}/${goarch}`);
  process.exit(1);
}
const platformDir = `stow-${goos}-${npmArch}`;
const packageDir = resolve(root, "packages", platformDir);
const manifestPath = resolve(packageDir, "package.json");
if (!existsSync(manifestPath)) {
  console.error(`missing platform package: packages/${platformDir}/package.json`);
  process.exit(1);
}
if (!existsSync(archive)) {
  console.error(`archive not found: ${archive}`);
  process.exit(1);
}

const manifest = JSON.parse(readFileSync(manifestPath, "utf8"));
if (manifest.os?.[0] !== goos || manifest.cpu?.[0] !== npmArch) {
  console.error(
    `${manifest.name} declares os=${JSON.stringify(manifest.os)} cpu=${JSON.stringify(manifest.cpu)}, ` +
      `but was packed from ${goos}/${npmArch}`,
  );
  process.exit(1);
}

// Extract the binary, stage it into the package, and pack. The staged copy lives
// in a gitignored path, so a committed manifest never claims to contain a
// binary that is not there.
const work = mkdtempSync(join(tmpdir(), "stow-platform-"));
try {
  const extracted = join(work, "stow");
  execFileSync("tar", ["-xzf", archive, "-C", work, "stow"], { stdio: "inherit" });
  const staged = join(packageDir, "bin", "stow");
  mkdirSync(join(packageDir, "bin"), { recursive: true });
  copyFileSync(extracted, staged);
  chmodSync(staged, 0o755);

  mkdirSync(outDir, { recursive: true });
  const packed = execFileSync("npm", ["pack", "--ignore-scripts", "--json", "--pack-destination", outDir], {
    cwd: packageDir,
    encoding: "utf8",
  });
  const [{ filename, size, files }] = JSON.parse(packed);
  if (!files.map((file) => file.path).includes("bin/stow-s3")) {
    console.error(`${manifest.name} packed without bin/stow-s3; refusing to publish`);
    process.exit(1);
  }
  console.log(`${manifest.name} (${goos}/${goarch}): ${filename} (${(size / 1048576).toFixed(1)} MB)`);
} finally {
  rmSync(work, { recursive: true, force: true });
  rmSync(resolve(packageDir, "bin"), { recursive: true, force: true });
}
