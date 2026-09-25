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

// The PyPI distribution ships the same binary, so it must be in step too. A skew
// here would publish a wheel whose binary is from a different release than the
// Python code that resolves it, which is the same failure the npm platform
// packages are checked for above.
const pythonProject = readFileSync(resolve(root, "packages/stow-py/pyproject.toml"), "utf8");
const pythonVersion = pythonProject.match(/^version = "([^"]+)"/m)?.[1];
if (!pythonVersion) {
  problems.push("packages/stow-py/pyproject.toml has no top-level version");
} else if (pythonVersion !== goVersion) {
  problems.push(
    `version mismatch: internal/version=${goVersion}, packages/stow-py=${pythonVersion}`,
  );
}

// The Python package reports its own __version__, and a user comparing it with
// the binary version needs the two to be the same number.
const pythonInit = readFileSync(resolve(root, "packages/stow-py/src/stow_s3/__init__.py"), "utf8");
const pythonInitVersion = pythonInit.match(/^__version__ = "([^"]+)"/m)?.[1];
if (pythonInitVersion !== pythonVersion) {
  problems.push(
    `stow_s3.__version__=${pythonInitVersion ?? "missing"} does not match pyproject version=${pythonVersion}`,
  );
}

// Outside a git work tree, such as a source export, there is no index to
// consult and the tracked-file check below is skipped.
function inGitWorkTree(cwd) {
  try {
    execFileSync("git", ["rev-parse", "--is-inside-work-tree"], { cwd, stdio: "ignore" });
    return true;
  } catch {
    return false;
  }
}
for (const platform of PLATFORM_PACKAGES) {
  const manifestPath = resolve(root, "packages", platform.dir, "package.json");
  if (!existsSync(manifestPath)) {
    problems.push(`missing platform package manifest: packages/${platform.dir}/package.json`);
    continue;
  }
  // A manifest that exists in the working tree but is untracked would pass every
  // other check here and still break a fresh clone, which is how these packages
  // were once left out of a commit while the local gate stayed green. Skipped
  // outside a git work tree, where a source export has no index to consult.
  if (inGitWorkTree(root)) {
    try {
      execFileSync("git", ["ls-files", "--error-unmatch", `packages/${platform.dir}/package.json`], {
        cwd: root,
        stdio: "ignore",
      });
    } catch {
      problems.push(`untracked platform package manifest: packages/${platform.dir}/package.json`);
      continue;
    }
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

// `stow doctor` reports whether a published binary exists for the running
// platform, so its list has to be the same list that actually ships. It lives in
// Go, in GOOS/GOARCH form, while the manifests use npm's cpu names, so the two
// are translated rather than compared as strings.
const NPM_CPU_TO_GOARCH = { x64: "amd64", arm64: "arm64" };
const doctorSource = readFileSync(resolve(root, "cmd/stow/doctor.go"), "utf8");
const doctorList = doctorSource.match(/var supportedPlatforms = \[\]string\{([^}]*)\}/s)?.[1];
if (!doctorList) {
  problems.push("cmd/stow/doctor.go no longer declares supportedPlatforms");
} else {
  const declaredPlatforms = [...doctorList.matchAll(/"([a-z0-9]+)\/([a-z0-9]+)"/g)].map((m) => `${m[1]}/${m[2]}`);
  const shippedPlatforms = PLATFORM_PACKAGES.map((p) => `${p.os}/${NPM_CPU_TO_GOARCH[p.cpu] ?? p.cpu}`);
  const missingFromDoctor = shippedPlatforms.filter((p) => !declaredPlatforms.includes(p));
  const missingFromRelease = declaredPlatforms.filter((p) => !shippedPlatforms.includes(p));
  if (missingFromDoctor.length > 0) {
    problems.push(`stow doctor omits shipped platforms: ${missingFromDoctor.join(", ")}`);
  }
  if (missingFromRelease.length > 0) {
    problems.push(`stow doctor claims platforms that are not shipped: ${missingFromRelease.join(", ")}`);
  }
}

if (problems.length > 0) {
  for (const problem of problems) console.error(problem);
  process.exit(1);
}
console.log(`Version source OK (${goVersion}); ${PLATFORM_PACKAGES.length} platform packages in step`);
