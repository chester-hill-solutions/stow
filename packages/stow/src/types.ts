import type { S3ClientConfig } from "@aws-sdk/client-s3";

export type StowMode = "local" | "run-through";

export interface UpstreamConfig {
  endpoint: string;
  accessKey: string;
  secretKey: string;
  region?: string;
  bucket?: string;
}

export interface StartOptions {
  dataDir?: string;
  buckets?: string[];
  port?: number;
  host?: string;
  cleanSlate?: boolean;
  accessKey?: string;
  secretKey?: string;
  /** When omitted, CLI auto-detects from env (STOW_* > S3_* > AWS_*). */
  mode?: StowMode | "auto";
  cacheDir?: string;
  allowLiveWrites?: boolean;
  /** Reserved: prefer env vars consumed by the Go CLI for run-through. */
  upstream?: UpstreamConfig;
}

export interface AwsSdkV3ConfigOptions {
  endpoint: string;
  accessKeyId: string;
  secretAccessKey: string;
  region?: string;
  forcePathStyle?: boolean;
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
  putFixture(
    bucket: string,
    key: string,
    body: string | Uint8Array | Buffer,
    options?: PutFixtureOptions,
  ): Promise<void>;
  snapshotObjects(bucket: string, prefix?: string): Promise<ObjectSnapshot[]>;
}
