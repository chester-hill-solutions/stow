import {
  BROWSER_PERSISTENCE_FORMAT_VERSION,
  BrowserPersistenceError,
  type PersistenceChange,
  type PersistenceSnapshot,
} from "./browser-types.js";
import type { EmbeddedBucket, EmbeddedObject } from "./embedded.js";

export function normalizeSnapshot(
  value: unknown,
  fallbackGeneration = 0,
): PersistenceSnapshot {
  if (!isRecord(value)) {
    throw corrupt("persistence snapshot is not an object");
  }
  const formatVersion = value.formatVersion ?? value.version;
  if (formatVersion !== BROWSER_PERSISTENCE_FORMAT_VERSION) {
    throw corrupt(`unsupported persistence format version ${String(formatVersion)}`);
  }
  const generation = value.generation === undefined ? fallbackGeneration : value.generation;
  if (!isGeneration(generation)) {
    throw corrupt("persistence snapshot has an invalid generation");
  }
  if (!Array.isArray(value.buckets) || !Array.isArray(value.objects)) {
    throw corrupt("persistence snapshot has invalid collections");
  }

  const buckets = value.buckets.map(normalizeBucket);
  const bucketNames = new Set<string>();
  for (const bucket of buckets) {
    if (bucketNames.has(bucket.name)) {
      throw corrupt(`persistence snapshot repeats bucket ${bucket.name}`);
    }
    bucketNames.add(bucket.name);
  }
  const objects = value.objects.map(normalizeObject);
  const objectKeys = new Set<string>();
  for (const object of objects) {
    if (!bucketNames.has(object.bucket)) {
      throw corrupt(`persistence object references missing bucket ${object.bucket}`);
    }
    const key = objectKey(object.bucket, object.key);
    if (objectKeys.has(key)) {
      throw corrupt(`persistence snapshot repeats object ${object.bucket}/${object.key}`);
    }
    objectKeys.add(key);
  }
  return {
    formatVersion: BROWSER_PERSISTENCE_FORMAT_VERSION,
    generation,
    buckets,
    objects,
  };
}

export function applyChanges(
  snapshot: PersistenceSnapshot,
  changes: PersistenceChange[],
): PersistenceSnapshot {
  const buckets = new Map(snapshot.buckets.map((bucket) => [bucket.name, { ...bucket }]));
  const objects = new Map(
    snapshot.objects.map((object) => [objectKey(object.bucket, object.key), normalizeObject(object)]),
  );
  for (const rawChange of changes) {
    const change = normalizeChange(rawChange);
    switch (change.type) {
      case "putBucket":
        buckets.set(change.bucket.name, { ...change.bucket });
        break;
      case "deleteBucket":
        buckets.delete(change.bucket);
        deleteBucketObjects(objects, change.bucket);
        break;
      case "putObject":
        objects.set(objectKey(change.object.bucket, change.object.key), normalizeObject(change.object));
        break;
      case "deleteObject":
        objects.delete(objectKey(change.bucket, change.key));
        break;
    }
  }
  return {
    formatVersion: BROWSER_PERSISTENCE_FORMAT_VERSION,
    generation: snapshot.generation,
    buckets: [...buckets.values()].sort((left, right) => left.name.localeCompare(right.name)),
    objects: [...objects.values()].sort(compareObjects),
  };
}

function deleteBucketObjects(
  objects: Map<string, EmbeddedObject>,
  bucket: string,
): void {
  for (const [key, object] of objects) {
    if (object.bucket === bucket) {
      objects.delete(key);
    }
  }
}

export function snapshotUsage(snapshot: PersistenceSnapshot): { bytes: number; objects: number } {
  return snapshot.objects.reduce(
    (usage, object) => ({
      bytes: usage.bytes + (object.data?.byteLength ?? 0),
      objects: usage.objects + 1,
    }),
    { bytes: 0, objects: 0 },
  );
}

export function emptySnapshot(generation: number): PersistenceSnapshot {
  return {
    formatVersion: BROWSER_PERSISTENCE_FORMAT_VERSION,
    generation,
    buckets: [],
    objects: [],
  };
}

export function normalizeChange(value: unknown): PersistenceChange {
  if (!isRecord(value)) {
    throw corrupt("persistence change is not an object");
  }
  const type = value.type;
  switch (type) {
    case "putBucket":
      return normalizePutBucket(type, value);
    case "deleteBucket":
      return normalizeDeleteBucket(type, value);
    case "putObject":
      return { type, object: normalizeObject(isRecord(value.object) ? value.object : value) };
    case "deleteObject":
      return normalizeDeleteObject(type, value);
    default:
      throw corrupt("persistence change has an unknown type");
  }
}

function normalizePutBucket(type: "putBucket", value: Record<string, unknown>): PersistenceChange {
  const bucket = isRecord(value.bucket)
    ? value.bucket
    : typeof value.bucket === "string"
      ? { name: value.bucket }
      : value;
  if (typeof bucket.name !== "string") {
    throw corrupt("persistence change has an invalid bucket");
  }
  return { type, bucket: normalizeBucket(bucket) };
}

function normalizeDeleteBucket(type: "deleteBucket", value: Record<string, unknown>): PersistenceChange {
  const name = typeof value.bucket === "string" ? value.bucket : value.name;
  if (typeof name !== "string" || name.length === 0) {
    throw corrupt("persistence change has an invalid bucket name");
  }
  return { type, bucket: name };
}

function normalizeDeleteObject(type: "deleteObject", value: Record<string, unknown>): PersistenceChange {
  if (typeof value.bucket !== "string" || typeof value.key !== "string") {
    throw corrupt("persistence change has an invalid object key");
  }
  return { type, bucket: value.bucket, key: value.key };
}

function normalizeBucket(value: unknown): EmbeddedBucket {
  if (!isRecord(value) || typeof value.name !== "string" || value.name.length === 0) {
    throw corrupt("persistence snapshot has an invalid bucket");
  }
  return {
    name: value.name,
    ...(typeof value.creationDate === "string" ? { creationDate: value.creationDate } : {}),
  };
}

function normalizeObject(value: unknown): EmbeddedObject {
  if (
    !isRecord(value) ||
    typeof value.bucket !== "string" ||
    value.bucket.length === 0 ||
    typeof value.key !== "string" ||
    value.key.length === 0
  ) {
    throw corrupt("persistence snapshot has an invalid object");
  }
  const data = copyBytes(value.data);
  if (typeof value.size === "number" && value.size !== data.byteLength) {
    throw corrupt(`persistence object ${value.bucket}/${value.key} has an invalid size`);
  }
  return {
    bucket: value.bucket,
    key: value.key,
    data,
    size: data.byteLength,
    etag: typeof value.etag === "string" ? value.etag : "",
    ...(typeof value.contentType === "string" ? { contentType: value.contentType } : {}),
    ...(isStringRecord(value.metadata) ? { metadata: { ...value.metadata } } : {}),
    ...(typeof value.lastModified === "string" ? { lastModified: value.lastModified } : {}),
  };
}

function copyBytes(value: unknown): Uint8Array {
  if (value instanceof Uint8Array) {
    return new Uint8Array(value);
  }
  if (value instanceof ArrayBuffer) {
    return new Uint8Array(value.slice(0));
  }
  if (ArrayBuffer.isView(value)) {
    return new Uint8Array(value.buffer, value.byteOffset, value.byteLength).slice();
  }
  if (
    Array.isArray(value) &&
    value.every((item) => Number.isInteger(item) && item >= 0 && item <= 255)
  ) {
    return Uint8Array.from(value);
  }
  throw corrupt("persistence object data is not bytes");
}

function isStringRecord(value: unknown): value is Record<string, string> {
  return isRecord(value) && Object.values(value).every((item) => typeof item === "string");
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

function isGeneration(value: unknown): value is number {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 0;
}

function objectKey(bucket: string, key: string): string {
  return `${bucket.length}:${bucket}${key}`;
}

function compareObjects(left: EmbeddedObject, right: EmbeddedObject): number {
  const bucketOrder = left.bucket.localeCompare(right.bucket);
  return bucketOrder === 0 ? left.key.localeCompare(right.key) : bucketOrder;
}

function corrupt(message: string): BrowserPersistenceError {
  return new BrowserPersistenceError("persistence_corrupt", message);
}
