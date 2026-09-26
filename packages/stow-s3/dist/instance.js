import { CreateBucketCommand, DeleteObjectCommand, GetObjectCommand, ListObjectsV2Command, PutObjectCommand, } from "@aws-sdk/client-s3";
import { createStowS3Client } from "./s3-client.js";
export const DEFAULT_REGION = "us-east-1";
// StowCredentialsError is thrown when a caller supplies neither static
// credentials nor a credential provider.
//
// The type asks for one of the two, so this is unreachable from TypeScript and
// exists for JavaScript callers and for values that arrived as `any`. It used to
// be reachable in a worse way: the configuration was built with an empty
// credentials object and handed to the AWS SDK, which failed later - at request
// time, several frames away, with a message about credential resolution. Being
// told what is wrong beats inferring it.
export class StowCredentialsError extends Error {
    constructor() {
        super("no credentials: supply accessKeyId and secretAccessKey, or a provider function");
        this.name = "StowCredentialsError";
    }
}
export function buildAwsSdkV3Config(options) {
    // A provider wins over static credentials. The type used to forbid supplying
    // both, which made the precedence below unreachable from the public API while
    // the implementation performed it and a test depended on it.
    const provider = "provider" in options ? options.provider : undefined;
    let credentials;
    if (provider) {
        credentials = provider;
    }
    else if (options.accessKeyId && options.secretAccessKey) {
        credentials = {
            accessKeyId: options.accessKeyId,
            secretAccessKey: options.secretAccessKey,
            ...(options.sessionToken === undefined
                ? {}
                : { sessionToken: options.sessionToken }),
        };
    }
    else {
        throw new StowCredentialsError();
    }
    return {
        endpoint: options.endpoint,
        region: options.region ?? DEFAULT_REGION,
        credentials,
        forcePathStyle: options.forcePathStyle ?? true,
    };
}
async function* listObjects(client, bucket, prefix) {
    let continuationToken;
    do {
        const list = await client.send(new ListObjectsV2Command({
            Bucket: bucket,
            Prefix: prefix,
            ContinuationToken: continuationToken,
        }));
        for (const object of list.Contents ?? []) {
            yield object;
        }
        continuationToken = list.IsTruncated
            ? list.NextContinuationToken
            : undefined;
    } while (continuationToken);
}
export function createStowInstance(options) {
    const region = options.region ?? DEFAULT_REGION;
    const s3Config = () => buildAwsSdkV3Config({
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
        createBucket: async (name) => {
            const client = createClient();
            try {
                await client.send(new CreateBucketCommand({ Bucket: name }));
            }
            finally {
                client.destroy();
            }
        },
        emptyBucket: async (name) => {
            const client = createClient();
            try {
                for await (const object of listObjects(client, name)) {
                    if (!object.Key) {
                        continue;
                    }
                    await client.send(new DeleteObjectCommand({ Bucket: name, Key: object.Key }));
                }
            }
            finally {
                client.destroy();
            }
        },
        putFixture: async (bucket, key, body, fixtureOptions) => {
            const client = createClient();
            try {
                await client.send(new PutObjectCommand({
                    Bucket: bucket,
                    Key: key,
                    Body: body,
                    ContentType: fixtureOptions?.contentType,
                    Metadata: fixtureOptions?.metadata,
                }));
            }
            finally {
                client.destroy();
            }
        },
        snapshotObjects: async (bucket, prefix) => {
            const client = createClient();
            try {
                const snapshots = [];
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
            }
            finally {
                client.destroy();
            }
        },
    };
}
export function createStowConnection(options) {
    const region = options.region ?? DEFAULT_REGION;
    const config = buildAwsSdkV3Config({ ...options, region });
    const client = createStowS3Client(config);
    let disconnected = false;
    return {
        endpoint: options.endpoint,
        accessKeyId: "provider" in options ? undefined : options.accessKeyId,
        secretAccessKey: "provider" in options ? undefined : options.secretAccessKey,
        ...("provider" in options || options.sessionToken === undefined
            ? {}
            : { sessionToken: options.sessionToken }),
        ...("provider" in options ? { provider: options.provider } : {}),
        region,
        client,
        awsSdkV3Config: () => config,
        disconnect: () => {
            if (!disconnected) {
                disconnected = true;
                client.destroy();
            }
        },
    };
}
export async function verifyObjectReadable(instance, bucket, key) {
    const client = createStowS3Client(instance.awsSdkV3Config());
    try {
        const response = await client.send(new GetObjectCommand({ Bucket: bucket, Key: key }));
        const body = await response.Body?.transformToString();
        if (body === undefined) {
            throw new Error(`Object ${bucket}/${key} returned no body`);
        }
        return body;
    }
    finally {
        client.destroy();
    }
}
//# sourceMappingURL=instance.js.map