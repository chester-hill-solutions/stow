import {
  BrowserPersistenceError,
  type IndexedDbPersistenceAdapterOptions,
  type LockManagerLike,
  type PersistenceAdapter,
  type PersistenceChange,
  type PersistenceSnapshot,
  validatePersistenceNamespace,
} from "./browser-types.js";
import {
  clearNamespace,
  commitChanges,
  openDatabase,
  readNamespace,
  type StoreNames,
} from "./indexeddb-store.js";
import { normalizeChange, normalizeSnapshot } from "./persistence-format.js";

const DEFAULT_DATABASE_NAME = "chs-stow";
const DEFAULT_MANIFEST_STORE = "manifest";
const DEFAULT_BUCKET_STORE = "buckets";
const DEFAULT_OBJECT_STORE = "objects";
const activeNamespaces = new Set<string>();

interface Ownership {
  release(): Promise<void>;
}

export class IndexedDbPersistenceAdapter implements PersistenceAdapter {
  private readonly factory: IDBFactory | undefined;
  private readonly databaseName: string;
  private readonly manifestStoreName: string;
  private readonly bucketStoreName: string;
  private readonly objectStoreName: string;
  private readonly lockManager: LockManagerLike | undefined;
  private readonly lockName: (namespace: string) => string;
  private database: IDBDatabase | undefined;
  private namespace: string | undefined;
  private ownership: Ownership | undefined;
  private closePromise: Promise<void> | undefined;
  private closed = false;

  constructor(options: IndexedDbPersistenceAdapterOptions = {}) {
    this.factory = options.indexedDB ?? globalIndexedDb();
    this.databaseName = options.databaseName ?? DEFAULT_DATABASE_NAME;
    this.manifestStoreName = options.manifestStoreName ?? DEFAULT_MANIFEST_STORE;
    this.bucketStoreName = options.bucketStoreName ?? DEFAULT_BUCKET_STORE;
    this.objectStoreName = options.objectStoreName ?? DEFAULT_OBJECT_STORE;
    this.lockManager = options.lockManager ?? globalLockManager();
    this.lockName = options.lockName ?? ((namespace) => `stow:${this.databaseName}:${namespace}`);
  }

  async open(namespace: string): Promise<void> {
    validatePersistenceNamespace(namespace);
    if (this.closed) {
      throw new BrowserPersistenceError("closed", "persistence adapter is closed");
    }
    if (this.namespace !== undefined) {
      throw new BrowserPersistenceError("already_open", "persistence namespace is already open");
    }
    const ownershipKey = this.lockName(namespace);
    this.ownership = await acquireOwnership(ownershipKey, this.lockManager);
    try {
      this.database = await openDatabase(
        this.factory,
        this.databaseName,
        this.manifestStoreName,
        this.bucketStoreName,
        this.objectStoreName,
      );
      this.namespace = namespace;
    } catch (error) {
      const ownership = this.ownership;
      this.ownership = undefined;
      if (ownership !== undefined) {
        try {
          await ownership.release();
        } catch {
          // Preserve the database-open failure.
        }
      }
      throw mapAdapterError(error);
    }
  }

  private get stores(): StoreNames {
    return {
      manifest: this.manifestStoreName,
      buckets: this.bucketStoreName,
      objects: this.objectStoreName,
    };
  }

  async load(namespace: string): Promise<PersistenceSnapshot | null> {
    this.ensureNamespace(namespace);
    const database = this.database;
    if (database === undefined) {
      throw new BrowserPersistenceError("persistence_error", "persistence database is not open");
    }
    try {
      const raw = await readNamespace(database, namespace, this.stores);
      return raw === null ? null : normalizeSnapshot(raw);
    } catch (error) {
      throw mapAdapterError(error);
    }
  }

  async commit(
    namespace: string,
    expectedGeneration: number,
    changes: PersistenceChange[],
  ): Promise<number> {
    this.ensureNamespace(namespace);
    validateGeneration(expectedGeneration);
    const normalizedChanges = changes.map(normalizeChange);
    const database = this.database;
    if (database === undefined) {
      throw new BrowserPersistenceError("persistence_error", "persistence database is not open");
    }
    try {
      return await commitChanges(database, namespace, expectedGeneration, normalizedChanges, this.stores);
    } catch (error) {
      throw mapAdapterError(error);
    }
  }

  async clear(namespace: string, expectedGeneration: number): Promise<number> {
    this.ensureNamespace(namespace);
    validateGeneration(expectedGeneration);
    const database = this.database;
    if (database === undefined) {
      throw new BrowserPersistenceError("persistence_error", "persistence database is not open");
    }
    try {
      return await clearNamespace(database, namespace, expectedGeneration, this.stores);
    } catch (error) {
      throw mapAdapterError(error);
    }
  }

  async close(): Promise<void> {
    this.closePromise ??= this.closeInternal();
    return this.closePromise;
  }

  private async closeInternal(): Promise<void> {
    this.closed = true;
    const database = this.database;
    const ownership = this.ownership;
    this.database = undefined;
    this.namespace = undefined;
    this.ownership = undefined;
    let firstError: unknown;
    if (database !== undefined) {
      try {
        database.close();
      } catch (error) {
        firstError = error;
      }
    }
    if (ownership !== undefined) {
      try {
        await ownership.release();
      } catch (error) {
        firstError ??= error;
      }
    }
    if (firstError !== undefined) {
      throw mapAdapterError(firstError);
    }
  }

  private ensureNamespace(namespace: string): void {
    if (this.closed) {
      throw new BrowserPersistenceError("closed", "persistence adapter is closed");
    }
    if (this.namespace === undefined) {
      throw new BrowserPersistenceError("persistence_error", "persistence namespace is not open");
    }
    if (this.namespace !== namespace) {
      throw new BrowserPersistenceError("persistence_error", "persistence namespace does not match");
    }
  }
}

function globalIndexedDb(): IDBFactory | undefined {
  const runtime = globalThis as typeof globalThis & { indexedDB?: IDBFactory };
  return runtime.indexedDB;
}

function globalLockManager(): LockManagerLike | undefined {
  const runtime = globalThis as typeof globalThis & {
    navigator?: { locks?: LockManagerLike };
  };
  return runtime.navigator?.locks;
}

async function acquireOwnership(
  ownershipKey: string,
  lockManager: LockManagerLike | undefined,
): Promise<Ownership> {
  if (activeNamespaces.has(ownershipKey)) {
    throw new BrowserPersistenceError("already_open", "persistence namespace is already open");
  }
  activeNamespaces.add(ownershipKey);
  if (lockManager === undefined) {
    return {
      release: async () => {
        activeNamespaces.delete(ownershipKey);
      },
    };
  }

  let releaseLock: (() => void) | undefined;
  const held = new Promise<void>((resolve) => {
    releaseLock = resolve;
  });
  let acquired = false;
  let rejectAcquired: ((error: unknown) => void) | undefined;
  let requestPromise: Promise<void> | undefined;
  const acquiredPromise = new Promise<void>((resolve, reject) => {
    rejectAcquired = reject;
    requestPromise = lockManager.request(
      ownershipKey,
      { mode: "exclusive", ifAvailable: true },
      async (lock) => {
        if (lock === null) {
          rejectAcquired?.(new BrowserPersistenceError("already_open", "persistence namespace is already open"));
          return;
        }
        acquired = true;
        resolve();
        await held;
      },
    );
    requestPromise.catch((error: unknown) => {
      if (!acquired) {
        activeNamespaces.delete(ownershipKey);
        rejectAcquired?.(error);
      }
    });
  });
  try {
    await acquiredPromise;
  } catch (error) {
    releaseLock?.();
    activeNamespaces.delete(ownershipKey);
    if (error instanceof BrowserPersistenceError) {
      throw error;
    }
    throw new BrowserPersistenceError("persistence_denied", `could not acquire persistence ownership: ${errorMessage(error)}`);
  }
  return {
    release: async () => {
      releaseLock?.();
      try {
        await requestPromise;
      } finally {
        activeNamespaces.delete(ownershipKey);
      }
    },
  };
}

function validateGeneration(generation: number): void {
  if (!Number.isSafeInteger(generation) || generation < 0) {
    throw new BrowserPersistenceError("persistence_error", "persistence generation must be a non-negative integer");
  }
}

function mapAdapterError(error: unknown): BrowserPersistenceError {
  if (error instanceof BrowserPersistenceError) {
    return error;
  }
  if (isRecord(error) && error.name === "QuotaExceededError") {
    return new BrowserPersistenceError("quota_exceeded", "IndexedDB quota exceeded");
  }
  if (isRecord(error) && error.name === "SecurityError") {
    return new BrowserPersistenceError("persistence_denied", "IndexedDB access was denied");
  }
  return new BrowserPersistenceError(
    "persistence_error",
    `IndexedDB persistence failed: ${errorMessage(error)}`,
  );
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}
