import { accessSync, constants, existsSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { packageBinaryPath } from "./ensure-binary.js";
const PACKAGE_ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "..");
function isExecutable(path) {
    try {
        accessSync(path, constants.X_OK);
        return true;
    }
    catch {
        return false;
    }
}
function findMonorepoBinary() {
    let dir = PACKAGE_ROOT;
    for (let depth = 0; depth < 8; depth += 1) {
        if (existsSync(join(dir, "go.mod"))) {
            const candidate = join(dir, "bin", "stow");
            if (isExecutable(candidate)) {
                return candidate;
            }
            return undefined;
        }
        const parent = dirname(dir);
        if (parent === dir) {
            break;
        }
        dir = parent;
    }
    return undefined;
}
function findPackageBinary() {
    const candidate = packageBinaryPath();
    if (isExecutable(candidate) || existsSync(candidate)) {
        return candidate;
    }
    return undefined;
}
/**
 * Resolve the stow binary path.
 *
 * Precedence: STOW_BIN env, package-local `bin/stow` (downloaded from release),
 * monorepo `bin/stow`, then `stow` on PATH.
 */
export function resolveStowBinary() {
    const fromEnv = process.env.STOW_BIN?.trim();
    if (fromEnv) {
        return fromEnv;
    }
    const fromPackage = findPackageBinary();
    if (fromPackage) {
        return fromPackage;
    }
    const fromRepo = findMonorepoBinary();
    if (fromRepo) {
        return fromRepo;
    }
    return "stow";
}
export function stowBinaryAvailable() {
    const bin = resolveStowBinary();
    if (bin !== "stow") {
        return isExecutable(bin) || existsSync(bin);
    }
    return false;
}
//# sourceMappingURL=bin.js.map