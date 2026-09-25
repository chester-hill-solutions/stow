import { BrowserPersistenceError, type PersistenceSnapshot } from "./browser-types.js";
import type { EmbeddedStow, EmbeddedStowOptions } from "./embedded.js";
export declare function validateQuotaOptions(options: EmbeddedStowOptions): void;
export declare function validateSnapshotQuota(snapshot: PersistenceSnapshot, options: EmbeddedStowOptions): void;
export declare function verifyRestored(embedded: EmbeddedStow, snapshot: PersistenceSnapshot): void;
export declare function restoreSnapshot(embedded: EmbeddedStow, snapshot: PersistenceSnapshot): void;
export declare function validateGeneration(generation: number): void;
export declare function normalizePersistenceError(error: unknown): BrowserPersistenceError;
export declare function isRecord(value: unknown): value is Record<string, unknown>;
//# sourceMappingURL=browser-validation.d.ts.map