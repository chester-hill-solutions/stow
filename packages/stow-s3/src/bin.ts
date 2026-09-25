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
      const candidate = join(dir, "bin", "stow-s3");
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
  "linux-x64": "@chester-hill-solutions/stow-s3-linux-x64",
  "linux-arm64": "@chester-hill-solutions/stow-s3-linux-arm64",
  "darwin-x64": "@chester-hill-solutions/stow-s3-darwin-x64",
  "darwin-arm64": "@chester-hill-solutions/stow-s3-darwin-arm64",
};

const BUNDLED_BINARY_SUBPATH = "bin/stow-s3";

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
 * Where a resolved binary path came from. The source is what a user needs when
 * a session will not start: "stow is not on PATH" and "STOW_BIN points at a
 * binary from an older release" are different problems with different fixes,
 * and the path alone does not distinguish them.
 */
export type StowBinarySource =
  | "platform-package"
  | "environment"
  | "monorepo"
  | "path";

export interface ResolvedStowBinary {
  readonly path: string;
  readonly source: StowBinarySource;
}

/** The platform package that should ship the binary here, if one is published. */
export function platformPackageForCurrentPlatform(): string | undefined {
  return PLATFORM_PACKAGES[`${process.platform}-${process.arch}`];
}

/**
 * Resolve the stow binary path and report which rule produced it.
 *
 * Precedence: a platform binary installed alongside this package, then the
 * STOW_BIN environment variable, then `bin/stow-s3` relative to a monorepo
 * root, then `stow-s3` on PATH. A plain `npm install` on a supported platform
 * therefore works with no setup.
 *
 * The platform package deliberately outranks STOW_BIN, and this comment used to
 * claim the opposite. It matters: a caller who upgrades the package and still
 * exports a STOW_BIN pointing at a binary from an older release should get the
 * binary that shipped with the version they installed, not the stale one their
 * shell happens to carry. A pinned install outranks a floating environment
 * variable. To exercise a specific build, point STOW_BIN at it in a tree with
 * no platform package installed, which is what this repository's own dev
 * install does by omitting optional dependencies.
 */
export function resolveStowBinaryDetailed(): ResolvedStowBinary {
  const bundled = findBundledBinary();
  if (bundled !== undefined) {
    return { path: bundled, source: "platform-package" };
  }

  const fromEnv = process.env.STOW_BIN?.trim();
  if (fromEnv) {
    return { path: fromEnv, source: "environment" };
  }

  const fromRepo = findMonorepoBinary();
  if (fromRepo) {
    return { path: fromRepo, source: "monorepo" };
  }

  return { path: "stow-s3", source: "path" };
}

export function resolveStowBinary(): string {
  return resolveStowBinaryDetailed().path;
}

export function stowBinaryAvailable(): boolean {
  const bin = resolveStowBinary();
  if (bin !== "stow-s3") {
    return isExecutable(bin);
  }
  return findOnPath("stow-s3") !== undefined;
}

/**
 * StowBinaryNotFoundError reports that no runnable server binary was found.
 * It exists because the underlying spawn failure is a bare ENOENT for the
 * literal string "stow-s3", which tells someone who installed from npm nothing
 * about why their session did not start or what to do about it.
 */
export class StowBinaryNotFoundError extends Error {
  readonly code = "binary_not_found";

  constructor(resolved: string) {
    super(
      `The stow-s3 server binary was not found (resolved to ${JSON.stringify(resolved)}). ` +
        "It is searched for in this order: the STOW_BIN environment variable, " +
        "bin/stow-s3 relative to a monorepo checkout, then stow-s3 on PATH. " +
        "Install the platform binary package for this platform, point STOW_BIN at an " +
        "existing binary, or use the EmbeddedStow and @chester-hill-solutions/stow-s3/browser profiles, " +
        "which need no server binary.",
    );
    this.name = "StowBinaryNotFoundError";
  }
}
