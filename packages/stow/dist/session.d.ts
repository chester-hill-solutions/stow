import { S3Client, type S3ClientConfig } from "@aws-sdk/client-s3";
/**
 * Default session limits, from the measured memory profile in
 * docs/benchmarks/session-baseline.md. At 4.59 MB of peak RSS per MiB of
 * stored data, 16 MiB implies roughly 85 MB per session.
 */
export declare const DEFAULT_SESSION_MAX_BYTES: number;
export declare const DEFAULT_SESSION_MAX_OBJECTS = 1000;
export interface StowSessionCapabilities {
    backend: string;
    persistent: boolean;
    multipart: boolean;
    upstream: boolean;
    maxBytes: number;
    maxObjects: number;
    maxRequestBytes: number;
    protocolVersion: number;
    binaryVersion: string;
}
export interface StowSession {
    /** The generated bucket created before this session was handed over. */
    readonly bucket: string;
    readonly endpoint: string;
    /**
     * Where this session keeps its data. The directory is removed on close unless
     * the caller supplied it, so this is mainly useful for diagnostics while a
     * session is open.
     */
    readonly dataDir: string;
    /**
     * A client owned by the session and destroyed on close. Callers that need a
     * longer-lived client should build one from awsSdkV3Config() and destroy it
     * themselves.
     */
    readonly s3: S3Client;
    awsSdkV3Config(): S3ClientConfig;
    capabilities(): StowSessionCapabilities;
    /**
     * A fresh environment mapping for a child process that needs to reach this
     * session. Never mutates the parent environment and never includes anything
     * beyond the endpoint, generated credentials, region, and bucket.
     */
    handoff(): Record<string, string>;
    close(): Promise<void>;
}
export interface EphemeralStowOptions {
    readonly maxBytes?: number;
    readonly maxObjects?: number;
    /** Override the generated bucket name. */
    readonly bucket?: string;
    readonly timeoutMs?: number;
    readonly signal?: AbortSignal;
    /** Keep session data in this directory instead of a temporary one. */
    readonly dataDir?: string;
}
/**
 * Acquire a disposable session: a local, memory-backed, loopback S3 endpoint
 * with a generated bucket and generated credentials, isolated from ambient
 * STOW_*, S3_*, and AWS_* configuration.
 */
export declare function openStow(options?: EphemeralStowOptions): Promise<StowSession>;
/**
 * Run a callback inside a session and always release it. The callback's error
 * wins over a cleanup failure so the original cause is never masked.
 */
export declare function withStow<T>(use: (session: StowSession) => T | Promise<T>, options?: EphemeralStowOptions): Promise<T>;
//# sourceMappingURL=session.d.ts.map