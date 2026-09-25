import { BrowserPersistenceError, } from "./browser-types.js";
import { replaySnapshot, snapshotUsage } from "./snapshot-replay.js";
const DEFAULT_MAX_BYTES = 64 << 20;
const DEFAULT_MAX_OBJECTS = 10_000;
export function validateQuotaOptions(options) {
    for (const value of [options.maxBytes, options.maxObjects]) {
        if (value !== undefined && (!Number.isSafeInteger(value) || value < 0)) {
            throw new BrowserPersistenceError("persistence_error", "browser persistence quotas must be non-negative integers");
        }
    }
}
export function validateSnapshotQuota(snapshot, options) {
    const usage = snapshotUsage(snapshot);
    const maxBytes = options.maxBytes === undefined || options.maxBytes === 0
        ? DEFAULT_MAX_BYTES
        : options.maxBytes;
    const maxObjects = options.maxObjects === undefined || options.maxObjects === 0
        ? DEFAULT_MAX_OBJECTS
        : options.maxObjects;
    if (usage.bytes > maxBytes || usage.objects > maxObjects) {
        throw new BrowserPersistenceError("quota_exceeded", "persisted browser data exceeds the requested quota");
    }
}
export function verifyRestored(embedded, snapshot) {
    const expected = snapshotUsage(snapshot);
    const actual = embedded.usage();
    if (actual.bytes !== expected.bytes || actual.objects !== expected.objects) {
        throw new BrowserPersistenceError("persistence_corrupt", "restored browser usage does not match the snapshot");
    }
    const capabilities = embedded.capabilities();
    if (actual.bytes > capabilities.maxBytes || actual.objects > capabilities.maxObjects) {
        throw new BrowserPersistenceError("quota_exceeded", "restored browser data exceeds the runtime quota");
    }
}
export function restoreSnapshot(embedded, snapshot) {
    embedded.reset();
    replaySnapshot(embedded, snapshot);
    verifyRestored(embedded, snapshot);
}
export function validateGeneration(generation) {
    if (!Number.isSafeInteger(generation) || generation < 0) {
        throw new BrowserPersistenceError("persistence_error", "persistence adapter returned an invalid generation");
    }
}
export function normalizePersistenceError(error) {
    if (error instanceof BrowserPersistenceError) {
        return error;
    }
    if (isRecord(error) && isStableCode(error.code)) {
        return new BrowserPersistenceError(error.code, errorMessage(error));
    }
    return new BrowserPersistenceError("persistence_error", errorMessage(error));
}
export function isRecord(value) {
    return typeof value === "object" && value !== null;
}
function isStableCode(value) {
    return (value === "persistence_unsupported" ||
        value === "persistence_denied" ||
        value === "already_open" ||
        value === "persistence_error" ||
        value === "persistence_corrupt" ||
        value === "quota_exceeded" ||
        value === "closed");
}
function errorMessage(error) {
    return error instanceof Error ? error.message : String(error);
}
//# sourceMappingURL=browser-validation.js.map