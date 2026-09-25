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
  EMBEDDED_PROTOCOL_VERSION,
  EmbeddedStow,
  EmbeddedStowError,
} from "./embedded.js";
// The browser persistence profile is deliberately not re-exported here. It is
// reachable only through the "./browser" subpath so that browser-only code
// never enters the Node entry point, and so the profile has exactly one
// documented import path.
export {
  loadNodeWasmHost,
  type NodeWasmHost,
} from "./node-wasm-host.js";
export type {
  EmbeddedBucket,
  EmbeddedCapabilities,
  EmbeddedHost,
  EmbeddedListOptions,
  EmbeddedObject,
  EmbeddedObjectPage,
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
export {
  DEFAULT_SESSION_MAX_BYTES,
  DEFAULT_SESSION_MAX_OBJECTS,
  openStow,
  withStow,
} from "./session.js";
export type {
  EphemeralStowOptions,
  StowSession,
  StowSessionCapabilities,
} from "./session.js";
export {
  READY_PROTOCOL_VERSION,
  StowProtocolError,
  parseReadyMessage,
} from "./ready.js";
export type {
  StowReady,
  StowReadyCapabilities,
} from "./ready.js";
export {
  StowBinaryNotFoundError,
  resolveStowBinary,
  stowBinaryAvailable,
} from "./bin.js";
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
