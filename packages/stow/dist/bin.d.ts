/**
 * Resolve the stow binary path.
 *
 * Precedence: STOW_BIN env, package-local `bin/stow` (downloaded from release),
 * monorepo `bin/stow`, then `stow` on PATH.
 */
export declare function resolveStowBinary(): string;
export declare function stowBinaryAvailable(): boolean;
//# sourceMappingURL=bin.d.ts.map