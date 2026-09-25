import type { EmbeddedHost } from "./embedded.js";
export interface NodeWasmHost extends EmbeddedHost {
    close(): Promise<void>;
}
export declare function loadNodeWasmHost(): Promise<NodeWasmHost>;
//# sourceMappingURL=node-wasm-host.d.ts.map