import type { S3ClientConfig } from "@aws-sdk/client-s3";
import { buildAwsSdkV3Config } from "./instance.js";
import { startStow } from "./start.js";
import type {
  AwsSdkV3ConfigOptions,
  StartOptions,
  StowInstance,
  UpstreamConfig,
} from "./types.js";
import { upstreamFromEnv } from "./upstream.js";

export type {
  AwsSdkV3ConfigOptions,
  ObjectSnapshot,
  PutFixtureOptions,
  StartOptions,
  StowInstance,
  StowMode,
  UpstreamConfig,
} from "./types.js";

export { parseReadyLine } from "./start.js";
export { resolveStowBinary, stowBinaryAvailable } from "./bin.js";
export { ensureStowBinary, packageBinaryPath } from "./ensure-binary.js";
export { upstreamFromEnv } from "./upstream.js";

export const Stow = {
  start(options?: StartOptions): Promise<StowInstance> {
    return startStow(options);
  },

  awsSdkV3Config(
    options: AwsSdkV3ConfigOptions | StowInstance,
  ): S3ClientConfig {
    if ("secretAccessKey" in options && "endpoint" in options) {
      return buildAwsSdkV3Config({
        endpoint: options.endpoint,
        accessKeyId: options.accessKeyId,
        secretAccessKey: options.secretAccessKey,
        region: options.region,
        forcePathStyle: true,
      });
    }
    return buildAwsSdkV3Config(options);
  },

  upstream: {
    fromEnv(): UpstreamConfig | null {
      return upstreamFromEnv();
    },
  },
};

export default Stow;
