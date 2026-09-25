import { BrowserPersistenceError, validatePersistenceNamespace, } from "./browser-types.js";
import { clearNamespace, commitChanges, openDatabase, readNamespace, } from "./indexeddb-store.js";
import { normalizeChange, normalizeSnapshot } from "./persistence-format.js";
const DEFAULT_DATABASE_NAME = "chs-stow";
const DEFAULT_MANIFEST_STORE = "manifest";
const DEFAULT_BUCKET_STORE = "buckets";
const DEFAULT_OBJECT_STORE = "objects";
const activeNamespaces = new Set();
export class IndexedDbPersistenceAdapter {
    factory;
    databaseName;
    manifestStoreName;
    bucketStoreName;
    objectStoreName;
    lockManager;
    lockName;
    database;
    namespace;
    ownership;
    closePromise;
    closed = false;
    constructor(options = {}) {
        this.factory = options.indexedDB ?? globalIndexedDb();
        this.databaseName = options.databaseName ?? DEFAULT_DATABASE_NAME;
        this.manifestStoreName = options.manifestStoreName ?? DEFAULT_MANIFEST_STORE;
        this.bucketStoreName = options.bucketStoreName ?? DEFAULT_BUCKET_STORE;
        this.objectStoreName = options.objectStoreName ?? DEFAULT_OBJECT_STORE;
        this.lockManager = options.lockManager ?? globalLockManager();
        this.lockName = options.lockName ?? ((namespace) => `stow:${this.databaseName}:${namespace}`);
    }
    async open(namespace) {
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
            this.database = await openDatabase(this.factory, this.databaseName, this.manifestStoreName, this.bucketStoreName, this.objectStoreName);
            this.namespace = namespace;
        }
        catch (error) {
            const ownership = this.ownership;
            this.ownership = undefined;
            if (ownership !== undefined) {
                try {
                    await ownership.release();
                }
                catch {
                    // Preserve the database-open failure.
                }
            }
            throw mapAdapterError(error);
        }
    }
    get stores() {
        return {
            manifest: this.manifestStoreName,
            buckets: this.bucketStoreName,
            objects: this.objectStoreName,
        };
    }
    async load(namespace) {
        this.ensureNamespace(namespace);
        const database = this.database;
        if (database === undefined) {
            throw new BrowserPersistenceError("persistence_error", "persistence database is not open");
        }
        try {
            const raw = await readNamespace(database, namespace, this.stores);
            return raw === null ? null : normalizeSnapshot(raw);
        }
        catch (error) {
            throw mapAdapterError(error);
        }
    }
    async commit(namespace, expectedGeneration, changes) {
        this.ensureNamespace(namespace);
        validateGeneration(expectedGeneration);
        const normalizedChanges = changes.map(normalizeChange);
        const database = this.database;
        if (database === undefined) {
            throw new BrowserPersistenceError("persistence_error", "persistence database is not open");
        }
        try {
            return await commitChanges(database, namespace, expectedGeneration, normalizedChanges, this.stores);
        }
        catch (error) {
            throw mapAdapterError(error);
        }
    }
    async clear(namespace, expectedGeneration) {
        this.ensureNamespace(namespace);
        validateGeneration(expectedGeneration);
        const database = this.database;
        if (database === undefined) {
            throw new BrowserPersistenceError("persistence_error", "persistence database is not open");
        }
        try {
            return await clearNamespace(database, namespace, expectedGeneration, this.stores);
        }
        catch (error) {
            throw mapAdapterError(error);
        }
    }
    async close() {
        this.closePromise ??= this.closeInternal();
        return this.closePromise;
    }
    async closeInternal() {
        this.closed = true;
        const database = this.database;
        const ownership = this.ownership;
        this.database = undefined;
        this.namespace = undefined;
        this.ownership = undefined;
        let firstError;
        if (database !== undefined) {
            try {
                database.close();
            }
            catch (error) {
                firstError = error;
            }
        }
        if (ownership !== undefined) {
            try {
                await ownership.release();
            }
            catch (error) {
                firstError ??= error;
            }
        }
        if (firstError !== undefined) {
            throw mapAdapterError(firstError);
        }
    }
    ensureNamespace(namespace) {
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
function globalIndexedDb() {
    const runtime = globalThis;
    return runtime.indexedDB;
}
function globalLockManager() {
    const runtime = globalThis;
    return runtime.navigator?.locks;
}
async function acquireOwnership(ownershipKey, lockManager) {
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
    let releaseLock;
    const held = new Promise((resolve) => {
        releaseLock = resolve;
    });
    let acquired = false;
    let rejectAcquired;
    let requestPromise;
    const acquiredPromise = new Promise((resolve, reject) => {
        rejectAcquired = reject;
        requestPromise = lockManager.request(ownershipKey, { mode: "exclusive", ifAvailable: true }, async (lock) => {
            if (lock === null) {
                rejectAcquired?.(new BrowserPersistenceError("already_open", "persistence namespace is already open"));
                return;
            }
            acquired = true;
            resolve();
            await held;
        });
        requestPromise.catch((error) => {
            if (!acquired) {
                activeNamespaces.delete(ownershipKey);
                rejectAcquired?.(error);
            }
        });
    });
    try {
        await acquiredPromise;
    }
    catch (error) {
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
            }
            finally {
                activeNamespaces.delete(ownershipKey);
            }
        },
    };
}
function validateGeneration(generation) {
    if (!Number.isSafeInteger(generation) || generation < 0) {
        throw new BrowserPersistenceError("persistence_error", "persistence generation must be a non-negative integer");
    }
}
function mapAdapterError(error) {
    if (error instanceof BrowserPersistenceError) {
        return error;
    }
    if (isRecord(error) && error.name === "QuotaExceededError") {
        return new BrowserPersistenceError("quota_exceeded", "IndexedDB quota exceeded");
    }
    if (isRecord(error) && error.name === "SecurityError") {
        return new BrowserPersistenceError("persistence_denied", "IndexedDB access was denied");
    }
    return new BrowserPersistenceError("persistence_error", `IndexedDB persistence failed: ${errorMessage(error)}`);
}
function isRecord(value) {
    return typeof value === "object" && value !== null;
}
function errorMessage(error) {
    return error instanceof Error ? error.message : String(error);
}
//# sourceMappingURL=indexeddb-persistence.js.map