import type { S3Client, S3ClientConfig } from "@aws-sdk/client-s3";
import type { AwsCredentialIdentityProvider } from "@smithy/types";
export type StowMode = "local" | "run-through";
export interface UpstreamConfig {
    endpoint: string;
    accessKey: string;
    secretKey: string;
    sessionToken?: string;
    region?: string;
    bucket?: string;
}
export interface StartOptions {
    dataDir?: string;
    backend?: "filesystem" | "memory";
    buckets?: string[];
    port?: number;
    host?: string;
    baseHost?: string;
    allowPublicAdmin?: boolean;
    cleanSlate?: boolean;
    accessKey?: string;
    secretKey?: string;
    /** When omitted, CLI auto-detects from env (STOW_* > S3_* > AWS_*). */
    mode?: StowMode | "auto";
    cacheDir?: string;
    cacheMaxBytes?: number;
    cacheMaxObjects?: number;
    cacheTtlSeconds?: number;
    allowLiveWrites?: boolean;
    /** Maximum stored bytes enforced on every request. Omit to leave the server unlimited. */
    maxBytes?: number;
    /** Maximum stored object count enforced on every request. Omit to leave the server unlimited. */
    maxObjects?: number;
}
type AwsSdkV3ConfigBase = {
    endpoint: string;
    region?: string;
    forcePathStyle?: boolean;
};
export type AwsSdkV3ConfigOptions = (AwsSdkV3ConfigBase & {
    accessKeyId: string;
    secretAccessKey: string;
    sessionToken?: string;
    provider?: never;
}) | (AwsSdkV3ConfigBase & {
    provider: AwsCredentialIdentityProvider;
    accessKeyId?: never;
    secretAccessKey?: never;
    sessionToken?: never;
});
export type ConnectOptions = AwsSdkV3ConfigOptions;
export interface StowConnection {
    endpoint: string;
    accessKeyId?: string;
    secretAccessKey?: string;
    sessionToken?: string;
    provider?: AwsCredentialIdentityProvider;
    region: string;
    client: S3Client;
    awsSdkV3Config(): S3ClientConfig;
    disconnect(): void;
}
export interface PutFixtureOptions {
    contentType?: string;
    metadata?: Record<string, string>;
}
export interface ObjectSnapshot {
    key: string;
    size: number;
    etag?: string;
    lastModified?: Date;
}
export interface StowInstance {
    endpoint: string;
    accessKeyId: string;
    secretAccessKey: string;
    region: string;
    mode: StowMode;
    dataDir: string;
    stop(): Promise<void>;
    awsSdkV3Config(): S3ClientConfig;
    createBucket(name: string): Promise<void>;
    emptyBucket(name: string): Promise<void>;
    putFixture(bucket: string, key: string, body: string | Uint8Array | Buffer, options?: PutFixtureOptions): Promise<void>;
    snapshotObjects(bucket: string, prefix?: string): Promise<ObjectSnapshot[]>;
}
export {};
//# sourceMappingURL=types.d.ts.map