import { type PersistenceChange } from "./browser-types.js";
export interface StoreNames {
    manifest: string;
    buckets: string;
    objects: string;
}
export declare function openDatabase(factory: IDBFactory | undefined, databaseName: string, manifestStoreName: string, bucketStoreName: string, objectStoreName: string): Promise<IDBDatabase>;
export declare function readNamespace(database: IDBDatabase, namespace: string, stores: StoreNames): Promise<unknown | null>;
export declare function commitChanges(database: IDBDatabase, namespace: string, expectedGeneration: number, changes: PersistenceChange[], stores: StoreNames): Promise<number>;
export declare function clearNamespace(database: IDBDatabase, namespace: string, expectedGeneration: number, stores: StoreNames): Promise<number>;
//# sourceMappingURL=indexeddb-store.d.ts.map