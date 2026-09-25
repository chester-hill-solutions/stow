import { type StowReady } from "./ready.js";
import type { StartOptions, StowInstance, StowMode } from "./types.js";
/**
 * The Go collector target a session's own server runs with.
 *
 * A session is short-lived and one of many on the machine, so its peak memory is
 * what matters and its throughput rarely is. The Python client must use the same
 * value; check-version.mjs fails if the two drift.
 */
export declare const SESSION_GOGC = "50";
export interface ReadyLine {
    endpoint: string;
    accessKeyId: string;
    secretAccessKey: string;
    mode: StowMode;
}
export declare function parseReadyLine(line: string): ReadyLine | null;
export declare function buildChildEnv(options: StartOptions): NodeJS.ProcessEnv;
export declare function startStow(options?: StartOptions): Promise<StowInstance>;
export interface StowStartup {
    instance: StowInstance;
    /**
     * The full readiness message. A session reports capabilities from this rather
     * than from its own assumptions, so the limit a caller is told about is the
     * limit the server is actually enforcing.
     */
    ready: StowReady;
}
export declare function startStowWithReady(options?: StartOptions): Promise<StowStartup>;
//# sourceMappingURL=start.d.ts.map