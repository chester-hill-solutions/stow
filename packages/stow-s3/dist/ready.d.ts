export declare const READY_PROTOCOL_VERSION = 1;
export interface StowReadyCapabilities {
    persistent: boolean;
    multipart: boolean;
    upstream: boolean;
    conditionalWrites: boolean;
    presignedUrls: boolean;
    maxBytes: number;
    maxObjects: number;
    maxRequestBytes: number;
}
export interface StowReady {
    protocolVersion: number;
    binaryVersion: string;
    endpoint: string;
    region: string;
    accessKeyId: string;
    secretAccessKey: string;
    mode: string;
    backend: string;
    capabilities: StowReadyCapabilities;
}
/**
 * Session lifecycle failure codes. These cover acquiring and releasing a
 * session only; S3 operation errors arrive from the AWS SDK unchanged so a
 * caller can keep matching on NoSuchKey, AccessDenied and the rest.
 */
export type StowErrorCode = "protocol_mismatch" | "cancelled" | "internal";
export declare class StowProtocolError extends Error {
    readonly code: StowErrorCode;
    constructor(code: StowErrorCode, message: string);
}
/**
 * Parse the single readiness object. Throws StowProtocolError on an unknown
 * protocol version, on a non-object payload, or on any missing field.
 */
export declare function parseReadyMessage(line: string): StowReady;
//# sourceMappingURL=ready.d.ts.map