export const EMBEDDED_PROTOCOL_VERSION = 1;

export interface EmbeddedHost {
  call(request: string): string;
}

export interface EmbeddedStowOptions {
  maxBytes?: number;
  maxObjects?: number;
}

export interface EmbeddedCapabilities {
  backend: "memory";
  maxBytes: number;
  maxObjects: number;
  persistent: boolean;
  multipart: boolean;
  upstream: boolean;
}

export interface EmbeddedUsage {
  bytes: number;
  objects: number;
}

export interface EmbeddedBucket {
  name: string;
  creationDate?: string;
}

export interface EmbeddedObject {
  bucket: string;
  key: string;
  data?: Uint8Array;
  size: number;
  etag: string;
  contentType?: string;
  metadata?: Record<string, string>;
  lastModified?: string;
}

export interface EmbeddedPutOptions {
  contentType?: string;
  metadata?: Record<string, string>;
}

export interface EmbeddedListOptions {
  prefix?: string;
  cursor?: string;
  limit?: number;
}

export interface EmbeddedObjectPage {
  objects: EmbeddedObject[];
  truncated: boolean;
  nextCursor?: string;
}

interface BridgeObject extends Omit<EmbeddedObject, "data"> {
  data?: string;
}

interface BridgeObjectPage {
  objects: BridgeObject[];
  truncated: boolean;
  nextCursor?: string;
}

interface OpenResult {
  handle: number;
  capabilities: EmbeddedCapabilities;
}

const base64Alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";

export class EmbeddedStowError extends Error {
  readonly code: string;

  constructor(code: string, message: string) {
    super(message);
    this.name = "EmbeddedStowError";
    this.code = code;
  }
}

export class EmbeddedStow {
  private closed = false;

  private constructor(
    private readonly host: EmbeddedHost,
    private readonly runtimeHandle: number,
    private readonly runtimeCapabilities: EmbeddedCapabilities,
  ) {}

  static open(host: EmbeddedHost, options: EmbeddedStowOptions = {}): EmbeddedStow {
    const result = invoke<OpenResult>(host, {
      version: EMBEDDED_PROTOCOL_VERSION,
      op: "open",
      options: {
        backend: "memory",
        maxBytes: options.maxBytes,
        maxObjects: options.maxObjects,
      },
    });
    return new EmbeddedStow(host, result.handle, result.capabilities);
  }

  get handle(): number {
    return this.runtimeHandle;
  }

  capabilities(): EmbeddedCapabilities {
    this.ensureOpen();
    return { ...this.runtimeCapabilities };
  }

  usage(): EmbeddedUsage {
    return this.invoke<EmbeddedUsage>({ op: "usage" });
  }

  createBucket(bucket: string): void {
    this.invoke<void>({ op: "createBucket", bucket });
  }

  deleteBucket(bucket: string): void {
    this.invoke<void>({ op: "deleteBucket", bucket });
  }

  listBuckets(): EmbeddedBucket[] {
    const result = this.invoke<{ buckets: EmbeddedBucket[] }>({ op: "listBuckets" });
    return result.buckets;
  }

  putObject(
    bucket: string,
    key: string,
    data: Uint8Array,
    options: EmbeddedPutOptions = {},
  ): EmbeddedObject {
    const result = this.invoke<BridgeObject>({
      op: "putObject",
      bucket,
      key,
      data: bytesToBase64(data),
      contentType: options.contentType,
      metadata: options.metadata,
    });
    return fromBridgeObject(result);
  }

  getObject(bucket: string, key: string): EmbeddedObject {
    return fromBridgeObject(
      this.invoke<BridgeObject>({ op: "getObject", bucket, key }),
    );
  }

  headObject(bucket: string, key: string): EmbeddedObject {
    return fromBridgeObject(
      this.invoke<BridgeObject>({ op: "headObject", bucket, key }),
    );
  }

  listObjects(bucket: string, options: EmbeddedListOptions = {}): EmbeddedObjectPage {
    const result = this.invoke<BridgeObjectPage>({
      op: "listObjects",
      bucket,
      list: { prefix: options.prefix, cursor: options.cursor, limit: options.limit },
    });
    return {
      objects: result.objects.map(fromBridgeObject),
      truncated: result.truncated,
      nextCursor: result.nextCursor,
    };
  }

  deleteObject(bucket: string, key: string): void {
    this.invoke<void>({ op: "deleteObject", bucket, key });
  }

  copyObject(
    sourceBucket: string,
    sourceKey: string,
    destinationBucket: string,
    destinationKey: string,
  ): EmbeddedObject {
    return fromBridgeObject(
      this.invoke<BridgeObject>({
        op: "copyObject",
        sourceBucket,
        sourceKey,
        destinationBucket,
        destinationKey,
      }),
    );
  }

  reset(): void {
    this.invoke<void>({ op: "reset" });
  }

  close(): void {
    if (this.closed) {
      return;
    }
    this.invoke<void>({ op: "close" });
    this.closed = true;
  }

  private invoke<T>(request: Record<string, unknown>): T {
    this.ensureOpen();
    return invoke<T>(this.host, {
      version: EMBEDDED_PROTOCOL_VERSION,
      ...request,
      handle: this.runtimeHandle,
    });
  }

  private ensureOpen(): void {
    if (this.closed) {
      throw new EmbeddedStowError("closed", "embedded runtime is closed");
    }
  }
}

function invoke<T>(host: EmbeddedHost, request: Record<string, unknown>): T {
  let parsed: unknown;
  try {
    parsed = JSON.parse(host.call(JSON.stringify(request)));
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    throw new EmbeddedStowError("host_error", `embedded host call failed: ${message}`);
  }
  if (!isRecord(parsed) || typeof parsed.version !== "number" || typeof parsed.ok !== "boolean") {
    throw new EmbeddedStowError("protocol", "embedded host returned an invalid response");
  }
  if (parsed.version !== EMBEDDED_PROTOCOL_VERSION) {
    throw new EmbeddedStowError(
      "protocol_version",
      `unsupported embedded protocol version ${parsed.version}`,
    );
  }
  if (!parsed.ok) {
    if (
      !isRecord(parsed.error) ||
      typeof parsed.error.code !== "string" ||
      typeof parsed.error.message !== "string"
    ) {
      throw new EmbeddedStowError("protocol", "embedded host returned an invalid error response");
    }
    throw new EmbeddedStowError(parsed.error.code, parsed.error.message);
  }
  return parsed.result as T;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

function fromBridgeObject(object: BridgeObject): EmbeddedObject {
  return {
    ...object,
    data: object.data === undefined ? undefined : base64ToBytes(object.data),
  };
}

function bytesToBase64(bytes: Uint8Array): string {
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

function base64ToBytes(value: string): Uint8Array {
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

function base64Value(character: string | undefined): number {
  if (character === undefined) {
    return 0;
  }
  const value = base64Alphabet.indexOf(character);
  if (value < 0) {
    throw new EmbeddedStowError("invalid_data", "invalid base64 object data");
  }
  return value;
}
