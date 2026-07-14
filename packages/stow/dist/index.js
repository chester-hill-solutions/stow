import { buildAwsSdkV3Config } from "./instance.js";
import { startStow } from "./start.js";
import { upstreamFromEnv } from "./upstream.js";
export { parseReadyLine } from "./start.js";
export { resolveStowBinary, stowBinaryAvailable } from "./bin.js";
export { upstreamFromEnv } from "./upstream.js";
export const Stow = {
    start(options) {
        return startStow(options);
    },
    awsSdkV3Config(options) {
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
        fromEnv() {
            return upstreamFromEnv();
        },
    },
};
export default Stow;
//# sourceMappingURL=index.js.map