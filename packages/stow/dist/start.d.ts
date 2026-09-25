import { type StowReady } from "./ready.js";
import type { StartOptions, StowInstance, StowMode } from "./types.js";
export interface ReadyLine {
    endpoint: string;
    accessKeyId: string;
    secretAccessKey: string;
    mode: StowMode;
}
export declare function parseReadyLine(line: string): ReadyLine | null;
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