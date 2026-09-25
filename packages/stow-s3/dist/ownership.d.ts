/**
 * The file a stow server writes into a data directory it has initialized.
 *
 * Duplicated in internal/storage/fs/owner.go, which is the writer. If the two
 * drift, every reset would silently refuse, so scripts/check-version.mjs fails
 * when they do.
 */
export declare const STOW_OWNER_MARKER = ".stow-owner";
/** A directory stow did not create is never deleted on a caller's behalf. */
export declare class StowOwnershipError extends Error {
    readonly dataDir: string;
    readonly code = "ownership";
    constructor(dataDir: string, reason: string);
}
/**
 * Delete a stow data directory that stow created, and nothing else.
 *
 * A directory that does not exist is already in the requested state, so that is
 * not an error. A directory that exists without the ownership marker is
 * refused: there is no evidence stow put it there, and a recursive delete is
 * not a recoverable mistake.
 */
export declare function resetOwnedData(dataDir: string): Promise<void>;
//# sourceMappingURL=ownership.d.ts.map