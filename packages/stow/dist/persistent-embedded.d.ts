import { type EmbeddedBucket, type EmbeddedCapabilities, type EmbeddedHost, type EmbeddedListOptions, type EmbeddedObject, type EmbeddedObjectPage, type EmbeddedPutOptions, type EmbeddedStowOptions, type EmbeddedUsage } from "./embedded.js";
import { type PersistenceAdapter, type PersistentEmbeddedStowOptions } from "./browser-types.js";
export declare class BrowserEmbeddedStow {
    private readonly embedded;
    private readonly adapter;
    private readonly namespace;
    private state;
    private tail;
    private closePromise;
    private durableSnapshot;
    private constructor();
    static open(host: EmbeddedHost, options: PersistentEmbeddedStowOptions): Promise<BrowserEmbeddedStow>;
    static create(host: EmbeddedHost, adapter: PersistenceAdapter, namespace: string, options?: EmbeddedStowOptions): Promise<BrowserEmbeddedStow>;
    get handle(): number;
    capabilities(): Promise<EmbeddedCapabilities>;
    usage(): Promise<EmbeddedUsage>;
    createBucket(bucket: string): Promise<void>;
    deleteBucket(bucket: string): Promise<void>;
    listBuckets(): Promise<EmbeddedBucket[]>;
    putObject(bucket: string, key: string, data: Uint8Array, options?: EmbeddedPutOptions): Promise<EmbeddedObject>;
    getObject(bucket: string, key: string): Promise<EmbeddedObject>;
    headObject(bucket: string, key: string): Promise<EmbeddedObject>;
    listObjects(bucket: string, options?: EmbeddedListOptions): Promise<EmbeddedObjectPage>;
    deleteObject(bucket: string, key: string): Promise<void>;
    copyObject(sourceBucket: string, sourceKey: string, destinationBucket: string, destinationKey: string): Promise<EmbeddedObject>;
    reset(): Promise<void>;
    close(): Promise<void>;
    private enqueue;
    private persistMutation;
    private resetInternal;
    private completeObject;
    private recoverDurableState;
    private poison;
    private closeResources;
    private stateError;
}
export declare function openBrowserEmbeddedStow(host: EmbeddedHost, options: PersistentEmbeddedStowOptions): Promise<BrowserEmbeddedStow>;
//# sourceMappingURL=persistent-embedded.d.ts.map