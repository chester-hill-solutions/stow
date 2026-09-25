import { accessSync, constants, existsSync } from "node:fs";
import { createRequire } from "node:module";
import { delimiter, dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const PACKAGE_ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "..");

// This package is ESM, so `require` is not in scope. createRequire gives us
// standard Node resolution, including a package's own exports map, without
// reimplementing node_modules lookup by hand.
const requireFromHere = createRequire(import.meta.url);

function isExecutable(path: string): boolean {
  try {
    accessSync(path, constants.X_OK);
    return true;
  } catch {
    return false;
  }
}

function findOnPath(name: string): string | undefined {
  const extensions =
    process.platform === "win32"
      ? (process.env.PATHEXT ?? ".EXE;.CMD;.BAT").split(";")
      : [""];
  for (const directory of (process.env.PATH ?? "").split(delimiter)) {
    if (!directory) {
      continue;
    }
    for (const extension of extensions) {
      const candidate = join(directory, `${name}${extension.toLowerCase()}`);
      if (isExecutable(candidate)) {
        return candidate;
      }
      if (process.platform !== "win32") {
        break;
      }
    }
  }
  return undefined;
}

function findMonorepoBinary(): string | undefined {
  let dir = PACKAGE_ROOT;
  for (let depth = 0; depth < 8; depth += 1) {
    if (existsSync(join(dir, "go.mod"))) {
      const candidate = join(dir, "bin", "stow");
      if (isExecutable(candidate)) {
        return candidate;
      }
    }
    const parent = dirname(dir);
    if (parent === dir) {
      break;
    }
    dir = parent;
  }
  return undefined;
}

// PLATFORM_PACKAGES maps a Node platform/arch pair to the optional npm package
// that ships the matching stow binary. It mirrors the platform list the release
// workflow builds, so an install on a supported platform resolves a binary with
// no PATH or environment setup.
const PLATFORM_PACKAGES: Record<string, string> = {
  "linux-x64": "@chs/stow-linux-x64",
  "linux-arm64": "@chs/stow-linux-arm64",
  "darwin-x64": "@chs/stow-darwin-x64",
  "darwin-arm64": "@chs/stow-darwin-arm64",
};

const BUNDLED_BINARY_SUBPATH = "bin/stow";

function findBundledBinary(): string | undefined {
  const platformPackage = PLATFORM_PACKAGES[`${process.platform}-${process.arch}`];
  if (platformPackage === undefined) {
    return undefined;
  }
  try {
    // Resolve through the platform package's exports map so the path works
    // whether it is installed flat or hoisted into a workspace.
    const entry = requireFromHere.resolve(`${platformPackage}/${BUNDLED_BINARY_SUBPATH}`);
    return isExecutable(entry) ? entry : undefined;
  } catch {
    // The optional package is absent for this platform, or npm skipped it.
    // Resolution continues with the remaining locations.
    return undefined;
  }
}

/**
 * Resolve the stow binary path.
 *
 * Precedence: a platform binary installed alongside this package, the STOW_BIN
 * environment variable, `bin/stow` relative to a monorepo root, then `stow` on
 * PATH. A plain `npm install` on a supported platform therefore works with no
 * setup, while an explicit STOW_BIN still wins for development and testing.
 */
export function resolveStowBinary(): string {
  const bundled = findBundledBinary();
  if (bundled !== undefined) {
    return bundled;
  }

  const fromEnv = process.env.STOW_BIN?.trim();
  if (fromEnv) {
    return fromEnv;
  }

  const fromRepo = findMonorepoBinary();
  if (fromRepo) {
    return fromRepo;
  }

  return "stow";
}

export function stowBinaryAvailable(): boolean {
  const bin = resolveStowBinary();
  if (bin !== "stow") {
    return isExecutable(bin);
  }
  return findOnPath("stow") !== undefined;
}

/**
 * StowBinaryNotFoundError reports that no runnable server binary was found.
 * It exists because the underlying spawn failure is a bare ENOENT for the
 * literal string "stow", which tells someone who installed from npm nothing
 * about why their session did not start or what to do about it.
 */
export class StowBinaryNotFoundError extends Error {
  readonly code = "binary_not_found";

  constructor(resolved: string) {
    super(
      `The stow server binary was not found (resolved to ${JSON.stringify(resolved)}). ` +
        "It is searched for in this order: the STOW_BIN environment variable, " +
        "bin/stow relative to a monorepo checkout, then stow on PATH. " +
        "Install the platform binary package for this platform, point STOW_BIN at an " +
        "existing binary, or use the EmbeddedStow and @chs/stow/browser profiles, " +
        "which need no server binary.",
    );
    this.name = "StowBinaryNotFoundError";
  }
}
