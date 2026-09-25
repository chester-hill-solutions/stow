import { BrowserPersistenceError, type PersistenceSnapshot } from "./browser-types.js";
import type { EmbeddedStow } from "./embedded.js";

export {
  applyChanges,
  emptySnapshot,
  normalizeChange,
  normalizeSnapshot,
  snapshotUsage,
} from "./persistence-format.js";

export function replaySnapshot(embedded: EmbeddedStow, snapshot: PersistenceSnapshot): void {
  for (const bucket of snapshot.buckets) {
    embedded.createBucket(bucket.name);
  }
  for (const object of snapshot.objects) {
    if (object.data === undefined) {
      throw new BrowserPersistenceError("persistence_corrupt", "persistence object has no data");
    }
    embedded.putObject(object.bucket, object.key, object.data, {
      contentType: object.contentType,
      metadata: object.metadata,
    });
  }
}
