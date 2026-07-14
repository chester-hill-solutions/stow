import { S3Client } from "@aws-sdk/client-s3";
import { UNSIGNED_PAYLOAD } from "@smithy/signature-v4";
const unsignedPayloadMiddleware = (next) => async (args) => {
    const request = args.request;
    request.headers ??= {};
    request.headers["x-amz-content-sha256"] = UNSIGNED_PAYLOAD;
    return next(args);
};
/**
 * S3 client configured for stow's SigV4 verifier (UNSIGNED-PAYLOAD).
 */
export function createStowS3Client(config) {
    const client = new S3Client(config);
    client.middlewareStack.add(unsignedPayloadMiddleware, {
        step: "build",
        name: "stowUnsignedPayload",
        priority: "high",
    });
    return client;
}
//# sourceMappingURL=s3-client.js.map