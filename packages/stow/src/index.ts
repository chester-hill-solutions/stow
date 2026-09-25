import type { S3ClientConfig } from "@aws-sdk/client-s3";
import { buildAwsSdkV3Config, createStowConnection } from "./instance.js";
import { startStow } from "./start.js";
import type {
  AwsSdkV3ConfigOptions,
  ConnectOptions,
  StartOptions,
  StowConnection,
  StowInstance,
  UpstreamConfig,
} from "./types.js";
import { upstreamFromEnv } from "./upstream.js";

export {
  EmbeddedStow,
  EmbeddedStowError,
} from "./embedded.js";
export type {
  EmbeddedBucket,
  EmbeddedCapabilities,
  EmbeddedHost,
  EmbeddedListOptions,
  EmbeddedObject,
  EmbeddedPutOptions,
  EmbeddedStowOptions,
  EmbeddedUsage,
} from "./embedded.js";

export type {
  AwsSdkV3ConfigOptions,
  ConnectOptions,
  ObjectSnapshot,
  PutFixtureOptions,
  StartOptions,
  StowConnection,
  StowInstance,
  StowMode,
  UpstreamConfig,
} from "./types.js";

export { parseReadyLine } from "./start.js";
export { resolveStowBinary, stowBinaryAvailable } from "./bin.js";
export { upstreamFromEnv } from "./upstream.js";

export const Stow = {
  start(options?: StartOptions): Promise<StowInstance> {
    return startStow(options);
  },

  connect(options: ConnectOptions): StowConnection {
    return createStowConnection(options);
  },

  awsSdkV3Config(
    options: AwsSdkV3ConfigOptions | StowInstance,
  ): S3ClientConfig {
    if ("stop" in options) {
      return buildAwsSdkV3Config({
        endpoint: options.endpoint,
        accessKeyId: options.accessKeyId,
        secretAccessKey: options.secretAccessKey,
        region: options.region,
        forcePathStyle: true,
      });
    }
    return buildAwsSdkV3Config({
      ...options,
      forcePathStyle: true,
    });
  },

  upstream: {
    fromEnv(): UpstreamConfig | null {
      return upstreamFromEnv();
    },
  },
};

export default Stow;
