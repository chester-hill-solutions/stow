export declare function packageBinaryPath(): string;
/**
 * Ensure a stow binary exists under the package `bin/` directory, downloading
 * from the GitHub release when missing. Returns the absolute path.
 */
export declare function ensureStowBinary(version?: string): Promise<string>;
//# sourceMappingURL=ensure-binary.d.ts.map