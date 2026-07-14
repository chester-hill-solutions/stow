import type { S3ClientConfig } from "@aws-sdk/client-s3";
import type { AwsSdkV3ConfigOptions, StartOptions, StowInstance, UpstreamConfig } from "./types.js";
export type { AwsSdkV3ConfigOptions, ObjectSnapshot, PutFixtureOptions, StartOptions, StowInstance, StowMode, UpstreamConfig, } from "./types.js";
export { parseReadyLine } from "./start.js";
export { resolveStowBinary, stowBinaryAvailable } from "./bin.js";
export { ensureStowBinary, packageBinaryPath } from "./ensure-binary.js";
export { upstreamFromEnv } from "./upstream.js";
export declare const Stow: {
    start(options?: StartOptions): Promise<StowInstance>;
    awsSdkV3Config(options: AwsSdkV3ConfigOptions | StowInstance): S3ClientConfig;
    upstream: {
        fromEnv(): UpstreamConfig | null;
    };
};
export default Stow;
//# sourceMappingURL=index.d.ts.map