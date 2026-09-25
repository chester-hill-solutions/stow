import type { S3ClientConfig } from "@aws-sdk/client-s3";
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
    allowLiveWrites?: boolean;
}
export interface AwsSdkV3ConfigOptions {
    endpoint: string;
    accessKeyId: string;
    secretAccessKey: string;
    sessionToken?: string;
    provider?: AwsCredentialIdentityProvider;
    region?: string;
    forcePathStyle?: boolean;
}
export type ConnectOptions = AwsSdkV3ConfigOptions;
export interface StowConnection {
    endpoint: string;
    accessKeyId: string;
    secretAccessKey: string;
    sessionToken?: string;
    provider?: AwsCredentialIdentityProvider;
    region: string;
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
//# sourceMappingURL=types.d.ts.map