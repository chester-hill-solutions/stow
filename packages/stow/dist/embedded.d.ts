export declare const EMBEDDED_PROTOCOL_VERSION = 1;
export interface EmbeddedHost {
    call(request: string): string;
}
export interface EmbeddedStowOptions {
    maxBytes?: number;
    maxObjects?: number;
}
export interface EmbeddedCapabilities {
    backend: "memory";
    maxBytes: number;
    maxObjects: number;
    persistent: boolean;
    multipart: boolean;
    upstream: boolean;
}
export interface EmbeddedUsage {
    bytes: number;
    objects: number;
}
export interface EmbeddedBucket {
    name: string;
    creationDate?: string;
}
export interface EmbeddedObject {
    bucket: string;
    key: string;
    data?: Uint8Array;
    size: number;
    etag: string;
    contentType?: string;
    metadata?: Record<string, string>;
    lastModified?: string;
}
export interface EmbeddedPutOptions {
    contentType?: string;
    metadata?: Record<string, string>;
}
export interface EmbeddedListOptions {
    prefix?: string;
    cursor?: string;
    limit?: number;
}
export interface EmbeddedObjectPage {
    objects: EmbeddedObject[];
    truncated: boolean;
    nextCursor?: string;
}
export declare class EmbeddedStowError extends Error {
    readonly code: string;
    constructor(code: string, message: string);
}
export declare class EmbeddedStow {
    private readonly host;
    private readonly runtimeHandle;
    private readonly runtimeCapabilities;
    private closed;
    private constructor();
    static open(host: EmbeddedHost, options?: EmbeddedStowOptions): EmbeddedStow;
    get handle(): number;
    capabilities(): EmbeddedCapabilities;
    usage(): EmbeddedUsage;
    createBucket(bucket: string): void;
    deleteBucket(bucket: string): void;
    listBuckets(): EmbeddedBucket[];
    putObject(bucket: string, key: string, data: Uint8Array, options?: EmbeddedPutOptions): EmbeddedObject;
    getObject(bucket: string, key: string): EmbeddedObject;
    headObject(bucket: string, key: string): EmbeddedObject;
    listObjects(bucket: string, options?: EmbeddedListOptions): EmbeddedObjectPage;
    deleteObject(bucket: string, key: string): void;
    copyObject(sourceBucket: string, sourceKey: string, destinationBucket: string, destinationKey: string): EmbeddedObject;
    reset(): void;
    close(): void;
    private invoke;
    private ensureOpen;
}
//# sourceMappingURL=embedded.d.ts.map