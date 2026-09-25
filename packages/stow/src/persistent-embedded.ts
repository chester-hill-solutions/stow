import {
  EmbeddedStow,
  type EmbeddedBucket,
  type EmbeddedCapabilities,
  type EmbeddedHost,
  type EmbeddedListOptions,
  type EmbeddedObject,
  type EmbeddedObjectPage,
  type EmbeddedPutOptions,
  type EmbeddedStowOptions,
  type EmbeddedUsage,
} from "./embedded.js";
import {
  BrowserPersistenceError,
  type PersistenceAdapter,
  type PersistenceSnapshot,
  type PersistenceChange,
  type PersistentEmbeddedStowOptions,
  validatePersistenceNamespace,
} from "./browser-types.js";
import {
  applyChanges,
  emptySnapshot,
  normalizeSnapshot,
  replaySnapshot,
} from "./snapshot-replay.js";
import {
  normalizePersistenceError,
  restoreSnapshot,
  validateGeneration,
  validateQuotaOptions,
  validateSnapshotQuota,
  verifyRestored,
} from "./browser-validation.js";

type ProfileState = "open" | "closing" | "closed" | "poisoned";

export class BrowserEmbeddedStow {
  private state: ProfileState = "open";
  private tail: Promise<void> = Promise.resolve();
  private closePromise: Promise<void> | undefined;
  private durableSnapshot: PersistenceSnapshot;

  private constructor(
    private readonly embedded: EmbeddedStow,
    private readonly adapter: PersistenceAdapter,
    private readonly namespace: string,
    durableSnapshot: PersistenceSnapshot,
  ) {
    this.durableSnapshot = durableSnapshot;
  }

  static open(
    host: EmbeddedHost,
    options: PersistentEmbeddedStowOptions,
  ): Promise<BrowserEmbeddedStow> {
    return openBrowserEmbeddedStow(host, options);
  }

  static async create(
    host: EmbeddedHost,
    adapter: PersistenceAdapter,
    namespace: string,
    options: EmbeddedStowOptions = {},
  ): Promise<BrowserEmbeddedStow> {
    validatePersistenceNamespace(namespace);
    validateQuotaOptions(options);
    let adapterOpened = false;
    let embedded: EmbeddedStow | undefined;
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
    } catch (error) {
      if (embedded !== undefined) {
        try {
          embedded.close();
        } catch {
          // Preserve the original open failure.
        }
      }
      if (adapterOpened) {
        try {
          await adapter.close();
        } catch {
          // Preserve the original open failure.
        }
      }
      throw normalizePersistenceError(error);
    }
  }

  get handle(): number {
    return this.embedded.handle;
  }

  capabilities(): Promise<EmbeddedCapabilities> {
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

  usage(): Promise<EmbeddedUsage> {
    return this.enqueue(() => this.embedded.usage());
  }

  createBucket(bucket: string): Promise<void> {
    return this.enqueue(async () => {
      await this.persistMutation(
        () => {
          this.embedded.createBucket(bucket);
          return this.embedded.listBuckets().find((item) => item.name === bucket) ?? { name: bucket };
        },
        (created) => [{ type: "putBucket", bucket: created }],
      );
    });
  }

  deleteBucket(bucket: string): Promise<void> {
    return this.enqueue(() => this.persistMutation(
      () => {
        this.embedded.deleteBucket(bucket);
        return undefined;
      },
      () => [{ type: "deleteBucket", bucket }],
    ));
  }

  listBuckets(): Promise<EmbeddedBucket[]> {
    return this.enqueue(() => this.embedded.listBuckets());
  }

  putObject(
    bucket: string,
    key: string,
    data: Uint8Array,
    options: EmbeddedPutOptions = {},
  ): Promise<EmbeddedObject> {
    return this.enqueue(() => this.persistMutation(
      () => this.embedded.putObject(bucket, key, data, options),
      (object) => [{
        type: "putObject",
        object: this.completeObject(bucket, key, object),
      }],
    ));
  }

  getObject(bucket: string, key: string): Promise<EmbeddedObject> {
    return this.enqueue(() => this.embedded.getObject(bucket, key));
  }

  headObject(bucket: string, key: string): Promise<EmbeddedObject> {
    return this.enqueue(() => this.embedded.headObject(bucket, key));
  }

  listObjects(bucket: string, options: EmbeddedListOptions = {}): Promise<EmbeddedObjectPage> {
    return this.enqueue(() => this.embedded.listObjects(bucket, options));
  }

  deleteObject(bucket: string, key: string): Promise<void> {
    return this.enqueue(() => this.persistMutation(
      () => {
        this.embedded.deleteObject(bucket, key);
        return undefined;
      },
      () => [{ type: "deleteObject", bucket, key }],
    ));
  }

  copyObject(
    sourceBucket: string,
    sourceKey: string,
    destinationBucket: string,
    destinationKey: string,
  ): Promise<EmbeddedObject> {
    return this.enqueue(() => this.persistMutation(
      () => this.embedded.copyObject(
        sourceBucket,
        sourceKey,
        destinationBucket,
        destinationKey,
      ),
      (object) => [{
        type: "putObject",
        object: this.completeObject(destinationBucket, destinationKey, object),
      }],
    ));
  }

  async reset(): Promise<void> {
    await this.enqueue(() => this.resetInternal());
  }

  close(): Promise<void> {
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

  private enqueue<T>(operation: () => T | Promise<T>): Promise<T> {
    if (this.state !== "open") {
      return Promise.reject(this.stateError());
    }
    const result = this.tail.then(operation);
    this.tail = result.then(
      () => undefined,
      () => undefined,
    );
    return result;
  }

  private async persistMutation<T>(
    mutate: () => T,
    changesForResult: (result: T) => PersistenceChange[],
  ): Promise<T> {
    const result = mutate();
    let nextSnapshot: PersistenceSnapshot;
    try {
      const changes = changesForResult(result);
      nextSnapshot = applyChanges(this.durableSnapshot, changes);
      const generation = await this.adapter.commit(
        this.namespace,
        this.durableSnapshot.generation,
        changes,
      );
      validateGeneration(generation);
      this.durableSnapshot = { ...nextSnapshot, generation };
      return result;
    } catch (error) {
      if (!(await this.recoverDurableState())) {
        throw new BrowserPersistenceError(
          "persistence_error",
          "persistence commit failed and durable state could not be restored",
        );
      }
      throw normalizePersistenceError(error);
    }
  }

  private async resetInternal(): Promise<void> {
    let memoryReset = false;
    try {
      this.embedded.reset();
      memoryReset = true;
      const generation = await this.adapter.clear(
        this.namespace,
        this.durableSnapshot.generation,
      );
      validateGeneration(generation);
      this.durableSnapshot = emptySnapshot(generation);
    } catch (error) {
      if (memoryReset && !(await this.recoverDurableState())) {
        throw new BrowserPersistenceError(
          "persistence_error",
          "persistence reset failed and durable state could not be restored",
        );
      }
      throw normalizePersistenceError(error);
    }
  }

  private completeObject(
    bucket: string,
    key: string,
    object: EmbeddedObject,
  ): EmbeddedObject {
    if (object.data !== undefined) {
      return { ...object, bucket, key, data: new Uint8Array(object.data) };
    }
    return this.embedded.getObject(bucket, key);
  }

  private async recoverDurableState(): Promise<boolean> {
    try {
      restoreSnapshot(this.embedded, this.durableSnapshot);
      return true;
    } catch {
      await this.poison();
      return false;
    }
  }

  private async poison(): Promise<void> {
    this.state = "poisoned";
    try {
      this.embedded.close();
    } catch {
      // The profile is already unusable.
    }
    try {
      await this.adapter.close();
    } catch {
      // The profile is already unusable.
    }
  }

  private async closeResources(): Promise<void> {
    let firstError: unknown;
    try {
      this.embedded.close();
    } catch (error) {
      firstError = error;
    }
    try {
      await this.adapter.close();
    } catch (error) {
      firstError ??= error;
    }
    if (firstError !== undefined) {
      throw normalizePersistenceError(firstError);
    }
  }

  private stateError(): BrowserPersistenceError {
    if (this.state === "poisoned") {
      return new BrowserPersistenceError("persistence_error", "browser embedded profile is poisoned");
    }
    return new BrowserPersistenceError("closed", "browser embedded profile is closed");
  }
}

export async function openBrowserEmbeddedStow(
  host: EmbeddedHost,
  options: PersistentEmbeddedStowOptions,
): Promise<BrowserEmbeddedStow> {
  const { namespace, persistence, ...embeddedOptions } = options;
  return BrowserEmbeddedStow.create(host, persistence, namespace, embeddedOptions);
}
