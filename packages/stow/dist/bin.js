import { accessSync, constants, existsSync } from "node:fs";
import { delimiter, dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
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
function findOnPath(name) {
    const extensions = process.platform === "win32"
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
function findMonorepoBinary() {
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
/**
 * Resolve the stow binary path.
 *
 * Precedence: STOW_BIN env, `bin/stow` relative to monorepo root, then `stow` on PATH.
 */
export function resolveStowBinary() {
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
export function stowBinaryAvailable() {
    const bin = resolveStowBinary();
    if (bin !== "stow") {
        return isExecutable(bin);
    }
    return findOnPath("stow") !== undefined;
}
//# sourceMappingURL=bin.js.map