import { EmbeddedStow, } from "./embedded.js";
import { BrowserPersistenceError, validatePersistenceNamespace, } from "./browser-types.js";
import { applyChanges, emptySnapshot, normalizeSnapshot, replaySnapshot, } from "./snapshot-replay.js";
import { normalizePersistenceError, restoreSnapshot, validateGeneration, validateQuotaOptions, validateSnapshotQuota, verifyRestored, } from "./browser-validation.js";
export class BrowserEmbeddedStow {
    embedded;
    adapter;
    namespace;
    state = "open";
    tail = Promise.resolve();
    closePromise;
    durableSnapshot;
    constructor(embedded, adapter, namespace, durableSnapshot) {
        this.embedded = embedded;
        this.adapter = adapter;
        this.namespace = namespace;
        this.durableSnapshot = durableSnapshot;
    }
    static open(host, options) {
        return openBrowserEmbeddedStow(host, options);
    }
    static async create(host, adapter, namespace, options = {}) {
        validatePersistenceNamespace(namespace);
        validateQuotaOptions(options);
        let adapterOpened = false;
        let embedded;
        try {
            await adapter.open(namespace);
            adapterOpened = true;
            const loaded = await adapter.load(namespace);
            const durable = loaded === null || loaded === undefined
                ? emptySnapshot(0)
                : normalizeSnapshot(loaded);
            validateSnapshotQuota(durable, options);
            embedded = EmbeddedStow.open(host, options);
            replaySnapshot(embedded, durable);
            verifyRestored(embedded, durable);
            return new BrowserEmbeddedStow(embedded, adapter, namespace, durable);
        }
        catch (error) {
            if (embedded !== undefined) {
                try {
                    embedded.close();
                }
                catch {
                    // Preserve the original open failure.
                }
            }
            if (adapterOpened) {
                try {
                    await adapter.close();
                }
                catch {
                    // Preserve the original open failure.
                }
            }
            throw normalizePersistenceError(error);
        }
    }
    get handle() {
        return this.embedded.handle;
    }
    capabilities() {
        return this.enqueue(() => {
            const capabilities = this.embedded.capabilities();
            return {
                ...capabilities,
                backend: "indexeddb",
                persistent: true,
                multipart: false,
                upstream: false,
            };
        });
    }
    usage() {
        return this.enqueue(() => this.embedded.usage());
    }
    createBucket(bucket) {
        return this.enqueue(async () => {
            await this.persistMutation(() => {
                this.embedded.createBucket(bucket);
                return this.embedded.listBuckets().find((item) => item.name === bucket) ?? { name: bucket };
            }, (created) => [{ type: "putBucket", bucket: created }]);
        });
    }
    deleteBucket(bucket) {
        return this.enqueue(() => this.persistMutation(() => {
            this.embedded.deleteBucket(bucket);
            return undefined;
        }, () => [{ type: "deleteBucket", bucket }]));
    }
    listBuckets() {
        return this.enqueue(() => this.embedded.listBuckets());
    }
    putObject(bucket, key, data, options = {}) {
        return this.enqueue(() => this.persistMutation(() => this.embedded.putObject(bucket, key, data, options), (object) => [{
                type: "putObject",
                object: this.completeObject(bucket, key, object),
            }]));
    }
    getObject(bucket, key) {
        return this.enqueue(() => this.embedded.getObject(bucket, key));
    }
    headObject(bucket, key) {
        return this.enqueue(() => this.embedded.headObject(bucket, key));
    }
    listObjects(bucket, options = {}) {
        return this.enqueue(() => this.embedded.listObjects(bucket, options));
    }
    deleteObject(bucket, key) {
        return this.enqueue(() => this.persistMutation(() => {
            this.embedded.deleteObject(bucket, key);
            return undefined;
        }, () => [{ type: "deleteObject", bucket, key }]));
    }
    copyObject(sourceBucket, sourceKey, destinationBucket, destinationKey) {
        return this.enqueue(() => this.persistMutation(() => this.embedded.copyObject(sourceBucket, sourceKey, destinationBucket, destinationKey), (object) => [{
                type: "putObject",
                object: this.completeObject(destinationBucket, destinationKey, object),
            }]));
    }
    async reset() {
        await this.enqueue(() => this.resetInternal());
    }
    close() {
        if (this.closePromise !== undefined) {
            return this.closePromise;
        }
        this.state = "closing";
        this.closePromise = this.tail
            .then(() => this.closeResources())
            .finally(() => {
            this.state = "closed";
        });
        return this.closePromise;
    }
    enqueue(operation) {
        if (this.state !== "open") {
            return Promise.reject(this.stateError());
        }
        const result = this.tail.then(operation);
        this.tail = result.then(() => undefined, () => undefined);
        return result;
    }
    async persistMutation(mutate, changesForResult) {
        const result = mutate();
        let nextSnapshot;
        try {
            const changes = changesForResult(result);
            nextSnapshot = applyChanges(this.durableSnapshot, changes);
            const generation = await this.adapter.commit(this.namespace, this.durableSnapshot.generation, changes);
            validateGeneration(generation);
            this.durableSnapshot = { ...nextSnapshot, generation };
            return result;
        }
        catch (error) {
            if (!(await this.recoverDurableState())) {
                throw new BrowserPersistenceError("persistence_error", "persistence commit failed and durable state could not be restored");
            }
            throw normalizePersistenceError(error);
        }
    }
    async resetInternal() {
        let memoryReset = false;
        try {
            this.embedded.reset();
            memoryReset = true;
            const generation = await this.adapter.clear(this.namespace, this.durableSnapshot.generation);
            validateGeneration(generation);
            this.durableSnapshot = emptySnapshot(generation);
        }
        catch (error) {
            if (memoryReset && !(await this.recoverDurableState())) {
                throw new BrowserPersistenceError("persistence_error", "persistence reset failed and durable state could not be restored");
            }
            throw normalizePersistenceError(error);
        }
    }
    completeObject(bucket, key, object) {
        if (object.data !== undefined) {
            return { ...object, bucket, key, data: new Uint8Array(object.data) };
        }
        return this.embedded.getObject(bucket, key);
    }
    async recoverDurableState() {
        try {
            restoreSnapshot(this.embedded, this.durableSnapshot);
            return true;
        }
        catch {
            await this.poison();
            return false;
        }
    }
    async poison() {
        this.state = "poisoned";
        try {
            this.embedded.close();
        }
        catch {
            // The profile is already unusable.
        }
        try {
            await this.adapter.close();
        }
        catch {
            // The profile is already unusable.
        }
    }
    async closeResources() {
        let firstError;
        try {
            this.embedded.close();
        }
        catch (error) {
            firstError = error;
        }
        try {
            await this.adapter.close();
        }
        catch (error) {
            firstError ??= error;
        }
        if (firstError !== undefined) {
            throw normalizePersistenceError(firstError);
        }
    }
    stateError() {
        if (this.state === "poisoned") {
            return new BrowserPersistenceError("persistence_error", "browser embedded profile is poisoned");
        }
        return new BrowserPersistenceError("closed", "browser embedded profile is closed");
    }
}
export async function openBrowserEmbeddedStow(host, options) {
    const { namespace, persistence, ...embeddedOptions } = options;
    return BrowserEmbeddedStow.create(host, persistence, namespace, embeddedOptions);
}
//# sourceMappingURL=persistent-embedded.js.map