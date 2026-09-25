export const BROWSER_PERSISTENCE_FORMAT_VERSION = 1;
export class BrowserPersistenceError extends Error {
    code;
    constructor(code, message) {
        super(message);
        this.name = "BrowserPersistenceError";
        this.code = code;
    }
}
export function validatePersistenceNamespace(namespace) {
    if (typeof namespace !== "string" || namespace.trim().length === 0) {
        throw new BrowserPersistenceError("persistence_error", "persistence namespace must be a non-empty string");
    }
}
//# sourceMappingURL=browser-types.js.map