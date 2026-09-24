import {
  CreateBucketCommand,
  DeleteObjectCommand,
  GetObjectCommand,
  ListObjectsV2Command,
  PutObjectCommand,
  type ListObjectsV2CommandOutput,
  type S3Client,
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
  const credentials = options.provider ?? {
    accessKeyId: options.accessKeyId,
    secretAccessKey: options.secretAccessKey,
    ...(options.sessionToken === undefined
      ? {}
      : { sessionToken: options.sessionToken }),
  };

  return {
    endpoint: options.endpoint,
    region: options.region ?? DEFAULT_REGION,
    credentials,
    forcePathStyle: options.forcePathStyle ?? true,
  };
}

type ListedObject = NonNullable<ListObjectsV2CommandOutput["Contents"]>[number];

async function* listObjects(
  client: S3Client,
  bucket: string,
  prefix?: string,
): AsyncGenerator<ListedObject, void, undefined> {
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
      yield object;
    }
    continuationToken = list.IsTruncated
      ? list.NextContinuationToken
      : undefined;
  } while (continuationToken);
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
      try {
        await client.send(new CreateBucketCommand({ Bucket: name }));
      } finally {
        client.destroy();
      }
    },
    emptyBucket: async (name: string) => {
      const client = createClient();
      try {
        for await (const object of listObjects(client, name)) {
          if (!object.Key) {
            continue;
          }
          await client.send(
            new DeleteObjectCommand({ Bucket: name, Key: object.Key }),
          );
        }
      } finally {
        client.destroy();
      }
    },
    putFixture: async (
      bucket: string,
      key: string,
      body: string | Uint8Array | Buffer,
      fixtureOptions?: PutFixtureOptions,
    ) => {
      const client = createClient();
      try {
        await client.send(
          new PutObjectCommand({
            Bucket: bucket,
            Key: key,
            Body: body,
            ContentType: fixtureOptions?.contentType,
            Metadata: fixtureOptions?.metadata,
          }),
        );
      } finally {
        client.destroy();
      }
    },
    snapshotObjects: async (bucket: string, prefix?: string) => {
      const client = createClient();
      try {
        const snapshots: ObjectSnapshot[] = [];
        for await (const object of listObjects(client, bucket, prefix)) {
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
        snapshots.sort((a, b) => a.key.localeCompare(b.key));
        return snapshots;
      } finally {
        client.destroy();
      }
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
    ...(options.sessionToken === undefined
      ? {}
      : { sessionToken: options.sessionToken }),
    ...(options.provider === undefined ? {} : { provider: options.provider }),
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
  try {
    const response = await client.send(
      new GetObjectCommand({ Bucket: bucket, Key: key }),
    );
    const body = await response.Body?.transformToString();
    if (body === undefined) {
      throw new Error(`Object ${bucket}/${key} returned no body`);
    }
    return body;
  } finally {
    client.destroy();
  }
}
