import { readFile } from "node:fs/promises";
import { setTimeout as delay } from "node:timers/promises";
import vm from "node:vm";
export async function loadNodeWasmHost() {
    const runtimeGlobal = globalThis;
    const wasmExec = await readFile(new URL("./wasm_exec.js", import.meta.url), "utf8");
    vm.runInThisContext(wasmExec, { filename: "wasm_exec.js" });
    const GoConstructor = runtimeGlobal.Go;
    if (GoConstructor === undefined) {
        throw new Error("Go WASM runtime support is unavailable");
    }
    const webAssembly = runtimeGlobal.WebAssembly;
    if (webAssembly === undefined) {
        throw new Error("WebAssembly is unavailable in this Node runtime");
    }
    const go = new GoConstructor();
    const wasmBytes = await readFile(new URL("./stow-runtime.wasm", import.meta.url));
    const { instance } = await webAssembly.instantiate(wasmBytes, go.importObject);
    const runPromise = go.run(instance);
    for (let attempt = 0; attempt < 100 && runtimeGlobal.stow === undefined; attempt += 1) {
        await delay(1);
    }
    const bridge = runtimeGlobal.stow;
    if (bridge === undefined) {
        throw new Error("Stow WASM runtime did not initialize");
    }
    let closed = false;
    return {
        call: (request) => bridge.call(request),
        close: async () => {
            if (closed) {
                return;
            }
            closed = true;
            bridge.exit();
            await runPromise;
        },
    };
}
//# sourceMappingURL=node-wasm-host.js.map