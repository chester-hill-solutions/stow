/**
 * Where a resolved binary path came from. The source is what a user needs when
 * a session will not start: "stow is not on PATH" and "STOW_BIN points at a
 * binary from an older release" are different problems with different fixes,
 * and the path alone does not distinguish them.
 */
export type StowBinarySource = "platform-package" | "environment" | "monorepo" | "path";
export interface ResolvedStowBinary {
    readonly path: string;
    readonly source: StowBinarySource;
}
/** The platform package that should ship the binary here, if one is published. */
export declare function platformPackageForCurrentPlatform(): string | undefined;
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
export declare function resolveStowBinaryDetailed(): ResolvedStowBinary;
export declare function resolveStowBinary(): string;
export declare function stowBinaryAvailable(): boolean;
/**
 * StowBinaryNotFoundError reports that no runnable server binary was found.
 * It exists because the underlying spawn failure is a bare ENOENT for the
 * literal string "stow-s3", which tells someone who installed from npm nothing
 * about why their session did not start or what to do about it.
 */
export declare class StowBinaryNotFoundError extends Error {
    readonly code = "binary_not_found";
    constructor(resolved: string);
}
//# sourceMappingURL=bin.d.ts.map