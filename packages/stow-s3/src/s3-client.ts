import {
  S3Client,
  type S3ClientConfig,
  type ServiceInputTypes,
  type ServiceOutputTypes,
} from "@aws-sdk/client-s3";
import type { BuildMiddleware } from "@smithy/types";
import { UNSIGNED_PAYLOAD } from "@smithy/signature-v4";

const unsignedPayloadMiddleware: BuildMiddleware<ServiceInputTypes, ServiceOutputTypes> =
  (next) => async (args) => {
    if (!isHttpRequest(args.request)) {
      return next(args);
    }
    args.request.headers ??= {};
    args.request.headers["x-amz-content-sha256"] = UNSIGNED_PAYLOAD;
    return next(args);
  };

function isHttpRequest(value: unknown): value is { headers?: Record<string, string> } {
  return typeof value === "object" && value !== null && "headers" in value;
}

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
