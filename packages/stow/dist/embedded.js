const base64Alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
export class EmbeddedStowError extends Error {
    constructor(message) {
        super(message);
        this.name = "EmbeddedStowError";
    }
}
export class EmbeddedStow {
    host;
    runtimeHandle;
    runtimeCapabilities;
    closed = false;
    constructor(host, runtimeHandle, runtimeCapabilities) {
        this.host = host;
        this.runtimeHandle = runtimeHandle;
        this.runtimeCapabilities = runtimeCapabilities;
    }
    static open(host, options = {}) {
        const result = invoke(host, {
            op: "open",
            options: {
                backend: "memory",
                maxBytes: options.maxBytes,
                maxObjects: options.maxObjects,
            },
        });
        return new EmbeddedStow(host, result.handle, result.capabilities);
    }
    get handle() {
        return this.runtimeHandle;
    }
    capabilities() {
        this.ensureOpen();
        return { ...this.runtimeCapabilities };
    }
    usage() {
        return this.invoke({ op: "usage" });
    }
    createBucket(bucket) {
        this.invoke({ op: "createBucket", bucket });
    }
    deleteBucket(bucket) {
        this.invoke({ op: "deleteBucket", bucket });
    }
    listBuckets() {
        const result = this.invoke({ op: "listBuckets" });
        return result.buckets;
    }
    putObject(bucket, key, data, options = {}) {
        const result = this.invoke({
            op: "putObject",
            bucket,
            key,
            data: bytesToBase64(data),
            contentType: options.contentType,
            metadata: options.metadata,
        });
        return fromBridgeObject(result);
    }
    getObject(bucket, key) {
        return fromBridgeObject(this.invoke({ op: "getObject", bucket, key }));
    }
    headObject(bucket, key) {
        return fromBridgeObject(this.invoke({ op: "headObject", bucket, key }));
    }
    listObjects(bucket, options = {}) {
        const result = this.invoke({
            op: "listObjects",
            bucket,
            list: { prefix: options.prefix, limit: options.limit },
        });
        return result.objects.map(fromBridgeObject);
    }
    deleteObject(bucket, key) {
        this.invoke({ op: "deleteObject", bucket, key });
    }
    copyObject(sourceBucket, sourceKey, destinationBucket, destinationKey) {
        return fromBridgeObject(this.invoke({
            op: "copyObject",
            sourceBucket,
            sourceKey,
            destinationBucket,
            destinationKey,
        }));
    }
    reset() {
        this.invoke({ op: "reset" });
    }
    close() {
        if (this.closed) {
            return;
        }
        this.invoke({ op: "close" });
        this.closed = true;
    }
    invoke(request) {
        this.ensureOpen();
        return invoke(this.host, { ...request, handle: this.runtimeHandle });
    }
    ensureOpen() {
        if (this.closed) {
            throw new EmbeddedStowError("embedded runtime is closed");
        }
    }
}
function invoke(host, request) {
    let response;
    try {
        response = JSON.parse(host.call(JSON.stringify(request)));
    }
    catch (error) {
        const message = error instanceof Error ? error.message : String(error);
        throw new EmbeddedStowError(`embedded host call failed: ${message}`);
    }
    if (!response.ok) {
        throw new EmbeddedStowError(response.error ?? "embedded runtime operation failed");
    }
    return response.result;
}
function fromBridgeObject(object) {
    return {
        ...object,
        data: object.data === undefined ? undefined : base64ToBytes(object.data),
    };
}
function bytesToBase64(bytes) {
    let result = "";
    for (let index = 0; index < bytes.length; index += 3) {
        const first = bytes[index] ?? 0;
        const hasSecond = index + 1 < bytes.length;
        const second = hasSecond ? bytes[index + 1] ?? 0 : 0;
        const hasThird = index + 2 < bytes.length;
        const third = hasThird ? bytes[index + 2] ?? 0 : 0;
        result += base64Alphabet.charAt(first >> 2);
        result += base64Alphabet.charAt(((first & 3) << 4) | (second >> 4));
        result += hasSecond ? base64Alphabet.charAt(((second & 15) << 2) | (third >> 6)) : "=";
        result += hasThird ? base64Alphabet.charAt(third & 63) : "=";
    }
    return result;
}
function base64ToBytes(value) {
    const normalized = value.replace(/[\r\n]/g, "");
    const padding = normalized.endsWith("==") ? 2 : normalized.endsWith("=") ? 1 : 0;
    const output = new Uint8Array(Math.max(0, Math.floor((normalized.length * 3) / 4) - padding));
    let outputIndex = 0;
    for (let index = 0; index < normalized.length; index += 4) {
        const first = base64Value(normalized[index]);
        const second = base64Value(normalized[index + 1]);
        const third = normalized[index + 2] === "=" ? 0 : base64Value(normalized[index + 2]);
        const fourth = normalized[index + 3] === "=" ? 0 : base64Value(normalized[index + 3]);
        if (outputIndex < output.length) {
            output[outputIndex++] = (first << 2) | (second >> 4);
        }
        if (normalized[index + 2] !== "=" && outputIndex < output.length) {
            output[outputIndex++] = ((second & 15) << 4) | (third >> 2);
        }
        if (normalized[index + 3] !== "=" && outputIndex < output.length) {
            output[outputIndex++] = ((third & 3) << 6) | fourth;
        }
    }
    return output;
}
function base64Value(character) {
    if (character === undefined) {
        return 0;
    }
    const value = base64Alphabet.indexOf(character);
    if (value < 0) {
        throw new EmbeddedStowError("invalid base64 object data");
    }
    return value;
}
//# sourceMappingURL=embedded.js.map