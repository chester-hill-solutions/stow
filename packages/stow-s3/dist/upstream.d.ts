import type { UpstreamConfig } from "./types.js";
/**
 * Resolve upstream credentials from environment variables.
 * Precedence: STOW_* > S3_* > AWS_*.
 */
export declare function upstreamFromEnv(): UpstreamConfig | null;
//# sourceMappingURL=upstream.d.ts.map