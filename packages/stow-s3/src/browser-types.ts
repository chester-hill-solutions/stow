import type {
  EmbeddedBucket,
  EmbeddedObject,
  EmbeddedStowOptions,
} from "./embedded.js";

export const BROWSER_PERSISTENCE_FORMAT_VERSION = 1 as const;

export type BrowserPersistenceErrorCode =
  | "persistence_unsupported"
  | "persistence_denied"
  | "already_open"
  | "persistence_error"
  | "persistence_corrupt"
  | "quota_exceeded"
  | "closed";

export class BrowserPersistenceError extends Error {
  readonly code: BrowserPersistenceErrorCode;

  constructor(code: BrowserPersistenceErrorCode, message: string) {
    super(message);
    this.name = "BrowserPersistenceError";
    this.code = code;
  }
}

export function validatePersistenceNamespace(namespace: string): void {
  if (typeof namespace !== "string" || namespace.trim().length === 0) {
    throw new BrowserPersistenceError("persistence_error", "persistence namespace must be a non-empty string");
  }
}

export interface PersistenceSnapshot {
  formatVersion: typeof BROWSER_PERSISTENCE_FORMAT_VERSION;
  generation: number;
  buckets: EmbeddedBucket[];
  objects: EmbeddedObject[];
}

export type PutBucketChange = {
  type: "putBucket";
  bucket: EmbeddedBucket;
};

export type DeleteBucketChange = {
  type: "deleteBucket";
  bucket: string;
};

export type PutObjectChange = {
  type: "putObject";
  object: EmbeddedObject;
};

export type DeleteObjectChange = {
  type: "deleteObject";
  bucket: string;
  key: string;
};

export type PersistenceChange =
  | PutBucketChange
  | DeleteBucketChange
  | PutObjectChange
  | DeleteObjectChange;

export interface PersistenceAdapter {
  open(namespace: string): Promise<void>;
  load(namespace: string): Promise<PersistenceSnapshot | null | undefined>;
  commit(
    namespace: string,
    expectedGeneration: number,
    changes: PersistenceChange[],
  ): Promise<number>;
  clear(namespace: string, expectedGeneration: number): Promise<number>;
  close(): Promise<void>;
}

export interface PersistentEmbeddedStowOptions extends EmbeddedStowOptions {
  namespace: string;
  persistence: PersistenceAdapter;
}

export interface IndexedDbPersistenceAdapterOptions {
  indexedDB?: IDBFactory;
  databaseName?: string;
  manifestStoreName?: string;
  bucketStoreName?: string;
  objectStoreName?: string;
  lockManager?: LockManagerLike;
  lockName?: (namespace: string) => string;
}

export interface LockManagerLike {
  request(
    name: string,
    options: { mode: "exclusive"; ifAvailable?: boolean },
    callback: (lock: unknown) => Promise<void>,
  ): Promise<void>;
}
