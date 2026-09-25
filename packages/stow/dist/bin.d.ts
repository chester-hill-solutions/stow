/**
 * Resolve the stow binary path.
 *
 * Precedence: a platform binary installed alongside this package, the STOW_BIN
 * environment variable, `bin/stow` relative to a monorepo root, then `stow` on
 * PATH. A plain `npm install` on a supported platform therefore works with no
 * setup, while an explicit STOW_BIN still wins for development and testing.
 */
export declare function resolveStowBinary(): string;
export declare function stowBinaryAvailable(): boolean;
/**
 * StowBinaryNotFoundError reports that no runnable server binary was found.
 * It exists because the underlying spawn failure is a bare ENOENT for the
 * literal string "stow", which tells someone who installed from npm nothing
 * about why their session did not start or what to do about it.
 */
export declare class StowBinaryNotFoundError extends Error {
    readonly code = "binary_not_found";
    constructor(resolved: string);
}
//# sourceMappingURL=bin.d.ts.map