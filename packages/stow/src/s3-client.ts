import { S3Client, type S3ClientConfig } from "@aws-sdk/client-s3";
import type { BuildMiddleware } from "@smithy/types";
import { UNSIGNED_PAYLOAD } from "@smithy/signature-v4";

const unsignedPayloadMiddleware: BuildMiddleware<any, any> =
  (next) => async (args) => {
    const request = args.request as { headers?: Record<string, string> };
    request.headers ??= {};
    request.headers["x-amz-content-sha256"] = UNSIGNED_PAYLOAD;
    return next(args);
  };

/**
 * S3 client configured for stow's SigV4 verifier (UNSIGNED-PAYLOAD).
 */
export function createStowS3Client(config: S3ClientConfig): S3Client {
  const client = new S3Client(config);
  client.middlewareStack.add(unsignedPayloadMiddleware, {
    step: "build",
    name: "stowUnsignedPayload",
    priority: "high",
  });
  return client;
}
