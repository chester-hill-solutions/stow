import { type PersistenceSnapshot } from "./browser-types.js";
import type { EmbeddedStow } from "./embedded.js";
export { applyChanges, emptySnapshot, normalizeChange, normalizeSnapshot, snapshotUsage, } from "./persistence-format.js";
export declare function replaySnapshot(embedded: EmbeddedStow, snapshot: PersistenceSnapshot): void;
//# sourceMappingURL=snapshot-replay.d.ts.map