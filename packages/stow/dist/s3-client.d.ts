import { S3Client, type S3ClientConfig } from "@aws-sdk/client-s3";
/**
 * S3 client configured for stow's SigV4 verifier (UNSIGNED-PAYLOAD).
 */
export declare function createStowS3Client(config: S3ClientConfig): S3Client;
//# sourceMappingURL=s3-client.d.ts.map