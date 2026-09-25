import { rm, stat } from "node:fs/promises";
import { homedir } from "node:os";
import { join, resolve, sep } from "node:path";
/**
 * The file a stow server writes into a data directory it has initialized.
 *
 * Duplicated in internal/storage/fs/owner.go, which is the writer. If the two
 * drift, every reset would silently refuse, so scripts/check-version.mjs fails
 * when they do.
 */
export const STOW_OWNER_MARKER = ".stow-owner";
/** A directory stow did not create is never deleted on a caller's behalf. */
export class StowOwnershipError extends Error {
    dataDir;
    code = "ownership";
    constructor(dataDir, reason) {
        super(`refusing to delete ${dataDir}: ${reason}`);
        this.dataDir = dataDir;
        this.name = "StowOwnershipError";
    }
}
/**
 * True when candidate is an ancestor of, or equal to, protectedDir.
 *
 * This is what makes the protected-path check hold against `dataDir: ".."`,
 * which resolves to a path that is not literally the home directory but lands
 * on one just the same.
 */
function isAncestorOrSelf(candidate, protectedDir) {
    const normalizedCandidate = resolve(candidate);
    const normalizedProtected = resolve(protectedDir);
    if (normalizedCandidate === normalizedProtected) {
        return true;
    }
    return normalizedProtected.startsWith(normalizedCandidate + sep);
}
/**
 * Reject paths whose deletion would destroy a developer's own work even if a
 * marker somehow existed there.
 *
 * The ownership marker is the primary defense and this is the backstop: it costs
 * nothing, and it is the layer that still holds when a marker has been copied,
 * a gitignore has hidden the real contents, or a caller has guessed a path
 * another stow process once used.
 */
function assertNotProtectedPath(dataDir) {
    const target = resolve(dataDir);
    const home = homedir();
    if (isAncestorOrSelf(target, home)) {
        throw new StowOwnershipError(dataDir, "it is your home directory or an ancestor of it. Pass a subdirectory stow created.");
    }
    if (isAncestorOrSelf(target, process.cwd())) {
        throw new StowOwnershipError(dataDir, "it is the current working directory or an ancestor of it. Pass a subdirectory stow created.");
    }
}
async function exists(path) {
    try {
        await stat(path);
        return true;
    }
    catch {
        return false;
    }
}
/**
 * Delete a stow data directory that stow created, and nothing else.
 *
 * A directory that does not exist is already in the requested state, so that is
 * not an error. A directory that exists without the ownership marker is
 * refused: there is no evidence stow put it there, and a recursive delete is
 * not a recoverable mistake.
 */
export async function resetOwnedData(dataDir) {
    assertNotProtectedPath(dataDir);
    const target = resolve(dataDir);
    if (!(await exists(target))) {
        return;
    }
    if (!(await exists(join(target, STOW_OWNER_MARKER)))) {
        throw new StowOwnershipError(dataDir, `it has no ${STOW_OWNER_MARKER} marker, so stow did not create it. ` +
            "Delete it yourself if that is what you want.");
    }
    await rm(target, { recursive: true, force: true });
}
//# sourceMappingURL=ownership.js.map