import type { S3ClientConfig } from "@aws-sdk/client-s3";
import type { AwsSdkV3ConfigOptions, ConnectOptions, StartOptions, StowConnection, StowInstance, UpstreamConfig } from "./types.js";
export { EMBEDDED_PROTOCOL_VERSION, EmbeddedStow, EmbeddedStowError, } from "./embedded.js";
export { loadNodeWasmHost, type NodeWasmHost, } from "./node-wasm-host.js";
export type { EmbeddedBucket, EmbeddedCapabilities, EmbeddedHost, EmbeddedListOptions, EmbeddedObject, EmbeddedObjectPage, EmbeddedPutOptions, EmbeddedStowOptions, EmbeddedUsage, } from "./embedded.js";
export type { AwsSdkV3ConfigOptions, ConnectOptions, ObjectSnapshot, PutFixtureOptions, StartOptions, StowConnection, StowInstance, StowMode, UpstreamConfig, } from "./types.js";
export { parseReadyLine } from "./start.js";
export { StowBinaryNotFoundError, resolveStowBinary, stowBinaryAvailable, } from "./bin.js";
export { upstreamFromEnv } from "./upstream.js";
export declare const Stow: {
    start(options?: StartOptions): Promise<StowInstance>;
    connect(options: ConnectOptions): StowConnection;
    awsSdkV3Config(options: AwsSdkV3ConfigOptions | StowInstance): S3ClientConfig;
    upstream: {
        fromEnv(): UpstreamConfig | null;
    };
};
export default Stow;
//# sourceMappingURL=index.d.ts.map