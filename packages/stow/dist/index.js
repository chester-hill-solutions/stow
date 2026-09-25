import { buildAwsSdkV3Config, createStowConnection } from "./instance.js";
import { startStow } from "./start.js";
import { upstreamFromEnv } from "./upstream.js";
export { EMBEDDED_PROTOCOL_VERSION, EmbeddedStow, EmbeddedStowError, } from "./embedded.js";
export { loadNodeWasmHost, } from "./node-wasm-host.js";
export { parseReadyLine } from "./start.js";
export { resolveStowBinary, stowBinaryAvailable } from "./bin.js";
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