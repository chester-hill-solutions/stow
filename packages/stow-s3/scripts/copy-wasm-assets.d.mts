// Types for the build scripts the tests import directly.
//
// scripts/copy-wasm-assets.mjs is plain JavaScript with no declaration file, so
// importing it from a type-checked test is an implicit `any`. Declaring the two
// functions the tests use is cheaper than enabling allowJs across the package,
// which would pull the whole build script directory into the program.

export declare function copyWasmAssets(options: {
  dist: string;
  wasm: string;
  wasmExec: string;
}): Promise<void>;

export declare function locateWasmExec(): Promise<string | null>;
