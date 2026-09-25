import { randomBytes } from "node:crypto";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { CreateBucketCommand, S3Client, } from "@aws-sdk/client-s3";
import { startStowWithReady } from "./start.js";
import { StowProtocolError } from "./ready.js";
/**
 * Default session limits, from the measured memory profile in
 * docs/benchmarks/session-baseline.md. At 4.59 MB of peak RSS per MiB of
 * stored data, 16 MiB implies roughly 85 MB per session.
 */
export const DEFAULT_SESSION_MAX_BYTES = 16 * 1024 * 1024;
export const DEFAULT_SESSION_MAX_OBJECTS = 1_000;
class SessionClosedError extends Error {
    code = "closed";
    constructor() {
        super("this stow session is closed");
        this.name = "StowSessionClosedError";
    }
}
function generatedBucketName() {
    return `stow-session-${randomBytes(8).toString("hex")}`;
}
function assertUsableLimits(options) {
    if (options.maxBytes !== undefined && (!Number.isSafeInteger(options.maxBytes) || options.maxBytes <= 0)) {
        throw new StowProtocolError("internal", "maxBytes must be a positive integer");
    }
    if (options.maxObjects !== undefined &&
        (!Number.isSafeInteger(options.maxObjects) || options.maxObjects <= 0)) {
        throw new StowProtocolError("internal", "maxObjects must be a positive integer");
    }
}
/**
 * Acquire a disposable session: a local, memory-backed, loopback S3 endpoint
 * with a generated bucket and generated credentials, isolated from ambient
 * STOW_*, S3_*, and AWS_* configuration.
 */
export async function openStow(options = {}) {
    assertUsableLimits(options);
    if (options.signal?.aborted) {
        throw new StowProtocolError("internal", "cancelled before the session started");
    }
    const ownsDataDir = options.dataDir === undefined;
    const dataDir = options.dataDir ?? (await mkdtemp(join(tmpdir(), "stow-session-")));
    const bucket = options.bucket ?? generatedBucketName();
    let startup;
    let client;
    try {
        startup = await startStowWithReady({
            mode: "local",
            backend: "memory",
            dataDir,
            maxBytes: options.maxBytes ?? DEFAULT_SESSION_MAX_BYTES,
            maxObjects: options.maxObjects ?? DEFAULT_SESSION_MAX_OBJECTS,
            isolatedEnvironment: true,
            // If this process is killed rather than closed, the server must not
            // outlive it.
            parentPid: process.pid,
            buckets: [bucket],
        });
        client = new S3Client(startup.instance.awsSdkV3Config());
        // startStow creates the bucket before the caller sees the session. Confirm it
        // through the client so a failure surfaces here rather than on the caller's
        // first operation.
        await client.send(new CreateBucketCommand({ Bucket: bucket }));
        return new Session(startup.instance, startup.ready, client, {
            bucket,
            ownsDataDir,
            dataDir,
        });
    }
    catch (error) {
        await disposeQuietly(startup?.instance, client, ownsDataDir, dataDir);
        throw error;
    }
}
/**
 * Run a callback inside a session and always release it. The callback's error
 * wins over a cleanup failure so the original cause is never masked.
 */
export async function withStow(use, options = {}) {
    const session = await openStow(options);
    let result;
    try {
        result = await use(session);
    }
    catch (callbackError) {
        try {
            await session.close();
        }
        catch (cleanupError) {
            throw new AggregateError([callbackError, cleanupError], "the session callback and its cleanup both failed");
        }
        throw callbackError;
    }
    await session.close();
    return result;
}
class Session {
    instance;
    ready;
    context;
    s3;
    state = "open";
    closePromise;
    constructor(instance, ready, client, context) {
        this.instance = instance;
        this.ready = ready;
        this.context = context;
        this.s3 = client;
    }
    get bucket() {
        this.assertOpen();
        return this.context.bucket;
    }
    get endpoint() {
        this.assertOpen();
        return this.instance.endpoint;
    }
    get dataDir() {
        return this.context.dataDir;
    }
    awsSdkV3Config() {
        this.assertOpen();
        return this.instance.awsSdkV3Config();
    }
    /**
     * Reported from the readiness message the server actually sent, so a caller
     * never sees a limit the server is not enforcing. A limit of 0 means the
     * server reported no limit, not that data is unlimited.
     */
    capabilities() {
        this.assertOpen();
        return {
            backend: this.ready.backend,
            persistent: this.ready.capabilities.persistent,
            multipart: this.ready.capabilities.multipart,
            upstream: this.ready.capabilities.upstream,
            maxBytes: this.ready.capabilities.maxBytes,
            maxObjects: this.ready.capabilities.maxObjects,
            maxRequestBytes: this.ready.capabilities.maxRequestBytes,
            protocolVersion: this.ready.protocolVersion,
            binaryVersion: this.ready.binaryVersion,
        };
    }
    handoff() {
        this.assertOpen();
        // The SDK types credentials as an identity or a provider function. A session
        // always configures a static identity, so anything else is a programming
        // error rather than a runtime condition to handle.
        const credentials = this.instance.awsSdkV3Config().credentials;
        if (typeof credentials !== "object" ||
            credentials === null ||
            typeof credentials.accessKeyId !== "string" ||
            typeof credentials.secretAccessKey !== "string") {
            throw new StowProtocolError("internal", "session does not hold static credentials to hand off");
        }
        // A new object every call, so a caller cannot mutate the session's view.
        return {
            AWS_ACCESS_KEY_ID: credentials.accessKeyId,
            AWS_SECRET_ACCESS_KEY: credentials.secretAccessKey,
            AWS_REGION: this.ready.region,
            AWS_ENDPOINT_URL: this.instance.endpoint,
            STOW_BUCKET: this.context.bucket,
        };
    }
    close() {
        if (this.closePromise) {
            return this.closePromise;
        }
        this.state = "closed";
        this.closePromise = (async () => {
            await this.s3.destroy();
            await this.instance.stop();
            if (this.context.ownsDataDir) {
                await rm(this.context.dataDir, { recursive: true, force: true });
            }
        })();
        return this.closePromise;
    }
    assertOpen() {
        if (this.state === "closed") {
            throw new SessionClosedError();
        }
    }
}
async function disposeQuietly(instance, client, ownsDataDir, dataDir) {
    try {
        await client?.destroy();
    }
    catch {
        // Cleanup during a failed start is best effort.
    }
    try {
        await instance?.stop();
    }
    catch {
        // Cleanup during a failed start is best effort.
    }
    if (ownsDataDir) {
        try {
            await rm(dataDir, { recursive: true, force: true });
        }
        catch {
            // Cleanup during a failed start is best effort.
        }
    }
}
//# sourceMappingURL=session.js.map