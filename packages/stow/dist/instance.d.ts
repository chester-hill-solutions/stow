import { type S3ClientConfig } from "@aws-sdk/client-s3";
import type { AwsSdkV3ConfigOptions, StowInstance, StowMode } from "./types.js";
export declare const DEFAULT_REGION = "us-east-1";
export declare function buildAwsSdkV3Config(options: AwsSdkV3ConfigOptions): S3ClientConfig;
export interface StowInstanceOptions {
    endpoint: string;
    accessKeyId: string;
    secretAccessKey: string;
    region?: string;
    mode: StowMode;
    dataDir: string;
    stopProcess: () => Promise<void>;
}
export declare function createStowInstance(options: StowInstanceOptions): StowInstance;
export declare function verifyObjectReadable(instance: StowInstance, bucket: string, key: string): Promise<string>;
//# sourceMappingURL=instance.d.ts.map