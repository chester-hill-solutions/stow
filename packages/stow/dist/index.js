import { buildAwsSdkV3Config, createStowConnection } from "./instance.js";
import { startStow } from "./start.js";
import { upstreamFromEnv } from "./upstream.js";
export { EMBEDDED_PROTOCOL_VERSION, EmbeddedStow, EmbeddedStowError, } from "./embedded.js";
// The browser persistence profile is deliberately not re-exported here. It is
// reachable only through the "./browser" subpath so that browser-only code
// never enters the Node entry point, and so the profile has exactly one
// documented import path.
export { loadNodeWasmHost, } from "./node-wasm-host.js";
export { parseReadyLine } from "./start.js";
export { DEFAULT_SESSION_MAX_BYTES, DEFAULT_SESSION_MAX_OBJECTS, openStow, withStow, } from "./session.js";
export { READY_PROTOCOL_VERSION, StowProtocolError, parseReadyMessage, } from "./ready.js";
export { StowBinaryNotFoundError, resolveStowBinary, stowBinaryAvailable, } from "./bin.js";
export { upstreamFromEnv } from "./upstream.js";
export const Stow = {
    start(options) {
        return startStow(options);
    },
    connect(options) {
        return createStowConnection(options);
    },
    awsSdkV3Config(options) {
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
        fromEnv() {
            return upstreamFromEnv();
        },
    },
};
export default Stow;
//# sourceMappingURL=index.js.map