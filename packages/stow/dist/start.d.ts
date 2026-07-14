import type { StartOptions, StowInstance, StowMode } from "./types.js";
export interface ReadyLine {
    endpoint: string;
    accessKeyId: string;
    secretAccessKey: string;
    mode: StowMode;
}
export declare function parseReadyLine(line: string): ReadyLine | null;
export declare function startStow(options?: StartOptions): Promise<StowInstance>;
//# sourceMappingURL=start.d.ts.map