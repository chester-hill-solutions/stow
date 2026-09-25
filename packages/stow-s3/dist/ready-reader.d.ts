import type { ChildProcess } from "node:child_process";
import { type StowReady } from "./ready.js";
import type { StowMode } from "./types.js";
export interface ReadyLine {
    endpoint: string;
    accessKeyId: string;
    secretAccessKey: string;
    mode: StowMode;
}
/**
 * Parse the pre-protocol single-line readiness announcement.
 *
 * Still read, because a server that ignores --ready-fd and writes the legacy
 * line to stdout is a server a client can still talk to. Its capabilities are
 * genuinely unknown in that case and are reported as unknown rather than
 * invented.
 */
export declare function parseReadyLine(line: string): ReadyLine | null;
export declare const READY_FD = 3;
interface ReadyResult {
    /** The fields the older instance type needs. */
    line: ReadyLine;
    /** The full protocol message, when it came from the versioned channel. */
    message: StowReady;
}
export declare function waitForReady(child: ChildProcess, timeoutMs?: number, signal?: AbortSignal): Promise<ReadyResult>;
export {};
//# sourceMappingURL=ready-reader.d.ts.map