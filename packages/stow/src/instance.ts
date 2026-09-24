import {
  CreateBucketCommand,
  DeleteObjectCommand,
  GetObjectCommand,
  ListObjectsV2Command,
  PutObjectCommand,
  type S3ClientConfig,
} from "@aws-sdk/client-s3";
import { createStowS3Client } from "./s3-client.js";
import type {
  AwsSdkV3ConfigOptions,
  ConnectOptions,
  ObjectSnapshot,
  PutFixtureOptions,
  StowConnection,
  StowInstance,
  StowMode,
} from "./types.js";

export const DEFAULT_REGION = "us-east-1";

export function buildAwsSdkV3Config(
  options: AwsSdkV3ConfigOptions,
): S3ClientConfig {
  return {
    endpoint: options.endpoint,
    region: options.region ?? DEFAULT_REGION,
    credentials: {
      accessKeyId: options.accessKeyId,
      secretAccessKey: options.secretAccessKey,
    },
    forcePathStyle: options.forcePathStyle ?? true,
  };
}

export interface StowInstanceOptions {
  endpoint: string;
  accessKeyId: string;
  secretAccessKey: string;
  region?: string;
  mode: StowMode;
  dataDir: string;
  stopProcess: () => Promise<void>;
}

export function createStowInstance(options: StowInstanceOptions): StowInstance {
  const region = options.region ?? DEFAULT_REGION;

  const s3Config = (): S3ClientConfig =>
    buildAwsSdkV3Config({
      endpoint: options.endpoint,
      accessKeyId: options.accessKeyId,
      secretAccessKey: options.secretAccessKey,
      region,
      forcePathStyle: true,
    });

  const createClient = () => createStowS3Client(s3Config());

  return {
    endpoint: options.endpoint,
    accessKeyId: options.accessKeyId,
    secretAccessKey: options.secretAccessKey,
    region,
    mode: options.mode,
    dataDir: options.dataDir,
    stop: options.stopProcess,
    awsSdkV3Config: s3Config,
    createBucket: async (name: string) => {
      const client = createClient();
      await client.send(new CreateBucketCommand({ Bucket: name }));
      client.destroy();
    },
    emptyBucket: async (name: string) => {
      const client = createClient();
      let continuationToken: string | undefined;
      do {
        const list = await client.send(
          new ListObjectsV2Command({
            Bucket: name,
            ContinuationToken: continuationToken,
          }),
        );
        for (const object of list.Contents ?? []) {
          if (!object.Key) {
            continue;
          }
          await client.send(
            new DeleteObjectCommand({ Bucket: name, Key: object.Key }),
          );
        }
        continuationToken = list.IsTruncated
          ? list.NextContinuationToken
          : undefined;
      } while (continuationToken);
      client.destroy();
    },
    putFixture: async (
      bucket: string,
      key: string,
      body: string | Uint8Array | Buffer,
      fixtureOptions?: PutFixtureOptions,
    ) => {
      const client = createClient();
      await client.send(
        new PutObjectCommand({
          Bucket: bucket,
          Key: key,
          Body: body,
          ContentType: fixtureOptions?.contentType,
          Metadata: fixtureOptions?.metadata,
        }),
      );
      client.destroy();
    },
    snapshotObjects: async (bucket: string, prefix?: string) => {
      const client = createClient();
      const snapshots: ObjectSnapshot[] = [];
      let continuationToken: string | undefined;
      do {
        const list = await client.send(
          new ListObjectsV2Command({
            Bucket: bucket,
            Prefix: prefix,
            ContinuationToken: continuationToken,
          }),
        );
        for (const object of list.Contents ?? []) {
          if (!object.Key) {
            continue;
          }
          snapshots.push({
            key: object.Key,
            size: object.Size ?? 0,
            etag: object.ETag,
            lastModified: object.LastModified,
          });
        }
        continuationToken = list.IsTruncated
          ? list.NextContinuationToken
          : undefined;
      } while (continuationToken);
      client.destroy();
      snapshots.sort((a, b) => a.key.localeCompare(b.key));
      return snapshots;
    },
  };
}

export function createStowConnection(options: ConnectOptions): StowConnection {
  const region = options.region ?? DEFAULT_REGION;
  const config = buildAwsSdkV3Config({ ...options, region });
  const client = createStowS3Client(config);
  let disconnected = false;
  return {
    endpoint: options.endpoint,
    accessKeyId: options.accessKeyId,
    secretAccessKey: options.secretAccessKey,
    region,
    awsSdkV3Config: () => config,
    disconnect: () => {
      if (!disconnected) {
        disconnected = true;
        client.destroy();
      }
    },
  };
}

export async function verifyObjectReadable(
  instance: StowInstance,
  bucket: string,
  key: string,
): Promise<string> {
  const client = createStowS3Client(instance.awsSdkV3Config());
  const response = await client.send(
    new GetObjectCommand({ Bucket: bucket, Key: key }),
  );
  const body = await response.Body?.transformToString();
  client.destroy();
  if (body === undefined) {
    throw new Error(`Object ${bucket}/${key} returned no body`);
  }
  return body;
}
