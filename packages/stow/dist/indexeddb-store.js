import { BrowserPersistenceError, BROWSER_PERSISTENCE_FORMAT_VERSION, } from "./browser-types.js";
const INDEXED_DB_VERSION = 1;
export function openDatabase(factory, databaseName, manifestStoreName, bucketStoreName, objectStoreName) {
    if (factory === undefined) {
        throw new BrowserPersistenceError("persistence_unsupported", "IndexedDB is not available in this host");
    }
    return new Promise((resolve, reject) => {
        const request = factory.open(databaseName, INDEXED_DB_VERSION);
        request.onupgradeneeded = () => {
            const database = request.result;
            ensureStore(database, manifestStoreName, "namespace");
            ensureStore(database, bucketStoreName, ["namespace", "name"]);
            ensureStore(database, objectStoreName, ["namespace", "bucket", "key"]);
        };
        request.onsuccess = () => {
            request.result.onversionchange = () => request.result.close();
            resolve(request.result);
        };
        request.onerror = () => reject(request.error ?? new Error("IndexedDB open failed"));
        request.onblocked = () => reject(new BrowserPersistenceError("persistence_denied", "IndexedDB open was blocked"));
    });
}
function ensureStore(database, name, keyPath) {
    if (!database.objectStoreNames.contains(name)) {
        database.createObjectStore(name, { keyPath });
    }
}
export function readNamespace(database, namespace, stores) {
    return new Promise((resolve, reject) => {
        const transaction = database.transaction([stores.manifest, stores.buckets, stores.objects], "readonly");
        const manifestRequest = transaction.objectStore(stores.manifest).get(namespace);
        const bucketRequest = transaction.objectStore(stores.buckets).getAll();
        const objectRequest = transaction.objectStore(stores.objects).getAll();
        let manifest;
        let buckets;
        let objects;
        manifestRequest.onsuccess = () => {
            manifest = manifestRequest.result;
        };
        bucketRequest.onsuccess = () => {
            buckets = bucketRequest.result;
        };
        objectRequest.onsuccess = () => {
            objects = objectRequest.result;
        };
        transaction.oncomplete = () => {
            try {
                resolve(toSnapshot(manifest, buckets, objects, namespace));
            }
            catch (error) {
                reject(error);
            }
        };
        transaction.onerror = () => reject(transaction.error ?? new Error("IndexedDB read failed"));
        transaction.onabort = () => reject(transaction.error ?? new Error("IndexedDB read aborted"));
    });
}
function toSnapshot(manifest, buckets, objects, namespace) {
    if (manifest === undefined || manifest === null) {
        if (Array.isArray(buckets) && buckets.some((record) => recordNamespace(record) === namespace)) {
            throw new BrowserPersistenceError("persistence_corrupt", "persistence records have no manifest");
        }
        if (Array.isArray(objects) && objects.some((record) => recordNamespace(record) === namespace)) {
            throw new BrowserPersistenceError("persistence_corrupt", "persistence records have no manifest");
        }
        return null;
    }
    if (!isRecord(manifest) || typeof manifest.generation !== "number") {
        throw new BrowserPersistenceError("persistence_corrupt", "persistence manifest is invalid");
    }
    if (!Array.isArray(buckets) || !Array.isArray(objects)) {
        throw new BrowserPersistenceError("persistence_corrupt", "persistence records are invalid");
    }
    return {
        formatVersion: manifestFormatVersion(manifest),
        generation: manifest.generation,
        buckets: buckets.filter((record) => recordNamespace(record) === namespace),
        objects: objects.filter((record) => recordNamespace(record) === namespace),
    };
}
export function commitChanges(database, namespace, expectedGeneration, changes, stores) {
    return runGenerationTransaction({
        database,
        namespace,
        expectedGeneration,
        stores,
        operation: ({ bucketStore, objectStore }) => {
            for (const change of changes) {
                applyChange(change, namespace, bucketStore, objectStore);
            }
        },
        failureMessage: "IndexedDB commit",
    });
}
function applyChange(change, namespace, bucketStore, objectStore) {
    switch (change.type) {
        case "putBucket":
            bucketStore.put({ ...change.bucket, namespace, name: change.bucket.name });
            break;
        case "deleteBucket":
            bucketStore.delete([namespace, change.bucket]);
            deleteNamespaceObjects(objectStore, namespace, (record) => record.bucket === change.bucket);
            break;
        case "putObject":
            objectStore.put({ namespace, ...change.object });
            break;
        case "deleteObject":
            objectStore.delete([namespace, change.bucket, change.key]);
            break;
    }
}
export function clearNamespace(database, namespace, expectedGeneration, stores) {
    return runGenerationTransaction({
        database,
        namespace,
        expectedGeneration,
        stores,
        operation: ({ bucketStore, objectStore }) => {
            deleteNamespaceBuckets(bucketStore, namespace);
            deleteNamespaceObjects(objectStore, namespace);
        },
        failureMessage: "IndexedDB clear",
    });
}
function runGenerationTransaction(options) {
    const { database, namespace, expectedGeneration, stores, operation, failureMessage } = options;
    return new Promise((resolve, reject) => {
        const transaction = database.transaction([stores.manifest, stores.buckets, stores.objects], "readwrite");
        const context = {
            manifestStore: transaction.objectStore(stores.manifest),
            bucketStore: transaction.objectStore(stores.buckets),
            objectStore: transaction.objectStore(stores.objects),
        };
        const manifestRequest = context.manifestStore.get(namespace);
        let settled = false;
        const fail = (error) => {
            if (settled)
                return;
            settled = true;
            try {
                transaction.abort();
            }
            catch {
                // The transaction may already have aborted.
            }
            reject(mapStoreError(error));
        };
        manifestRequest.onsuccess = () => {
            try {
                const current = manifestGeneration(manifestRequest.result);
                if (current !== expectedGeneration) {
                    throw new BrowserPersistenceError("persistence_error", `persistence generation conflict: expected ${expectedGeneration}, found ${current}`);
                }
                operation(context);
                context.manifestStore.put({
                    namespace,
                    formatVersion: BROWSER_PERSISTENCE_FORMAT_VERSION,
                    generation: expectedGeneration + 1,
                });
            }
            catch (error) {
                fail(error);
            }
        };
        transaction.oncomplete = () => {
            if (!settled) {
                settled = true;
                resolve(expectedGeneration + 1);
            }
        };
        transaction.onerror = () => fail(transaction.error ?? new Error(`${failureMessage} failed`));
        transaction.onabort = () => fail(new Error(`${failureMessage} aborted`));
    });
}
function manifestFormatVersion(value) {
    return value.formatVersion ?? value.version;
}
function manifestGeneration(value) {
    if (value === undefined || value === null) {
        return 0;
    }
    if (!isRecord(value) ||
        manifestFormatVersion(value) !== BROWSER_PERSISTENCE_FORMAT_VERSION ||
        typeof value.generation !== "number" ||
        !Number.isSafeInteger(value.generation) ||
        value.generation < 0) {
        throw new BrowserPersistenceError("persistence_corrupt", "persistence manifest is invalid");
    }
    return value.generation;
}
function deleteNamespaceBuckets(store, namespace) {
    const request = store.getAll();
    request.onsuccess = () => {
        for (const record of request.result) {
            if (recordNamespace(record) === namespace) {
                const value = asRecord(record);
                if (typeof value.name === "string") {
                    store.delete([namespace, value.name]);
                }
            }
        }
    };
}
function deleteNamespaceObjects(store, namespace, predicate = () => true) {
    const request = store.getAll();
    request.onsuccess = () => {
        for (const record of request.result) {
            if (recordNamespace(record) === namespace && predicate(asRecord(record))) {
                const value = asRecord(record);
                if (typeof value.bucket === "string" && typeof value.key === "string") {
                    store.delete([namespace, value.bucket, value.key]);
                }
            }
        }
    };
}
function recordNamespace(value) {
    return isRecord(value) ? value.namespace : undefined;
}
function asRecord(value) {
    return isRecord(value) ? value : {};
}
function isRecord(value) {
    return typeof value === "object" && value !== null;
}
function mapStoreError(error) {
    if (error instanceof BrowserPersistenceError) {
        return error;
    }
    return new BrowserPersistenceError("persistence_error", `IndexedDB transaction failed: ${error instanceof Error ? error.message : String(error)}`);
}
//# sourceMappingURL=indexeddb-store.js.map