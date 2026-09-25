import { type PersistenceChange, type PersistenceSnapshot } from "./browser-types.js";
export declare function normalizeSnapshot(value: unknown, fallbackGeneration?: number): PersistenceSnapshot;
export declare function applyChanges(snapshot: PersistenceSnapshot, changes: PersistenceChange[]): PersistenceSnapshot;
export declare function snapshotUsage(snapshot: PersistenceSnapshot): {
    bytes: number;
    objects: number;
};
export declare function emptySnapshot(generation: number): PersistenceSnapshot;
export declare function normalizeChange(value: unknown): PersistenceChange;
//# sourceMappingURL=persistence-format.d.ts.map