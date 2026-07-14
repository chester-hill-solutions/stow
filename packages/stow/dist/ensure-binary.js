import { chmodSync, createWriteStream, existsSync, mkdirSync, } from "node:fs";
import { dirname, join } from "node:path";
import { pipeline } from "node:stream/promises";
import { fileURLToPath } from "node:url";
import { Readable } from "node:stream";
const PACKAGE_ROOT = join(dirname(fileURLToPath(import.meta.url)), "..");
const DEFAULT_VERSION = "0.1.0";
const REPO = "chester-hill-solutions/stow";
export function packageBinaryPath() {
    const name = process.platform === "win32" ? "stow.exe" : "stow";
    return join(PACKAGE_ROOT, "bin", name);
}
function releaseAssetName() {
    const platform = process.platform === "darwin"
        ? "darwin"
        : process.platform === "linux"
            ? "linux"
            : process.platform === "win32"
                ? "windows"
                : null;
    const arch = process.arch === "arm64"
        ? "arm64"
        : process.arch === "x64"
            ? "amd64"
            : null;
    if (!platform || !arch) {
        throw new Error(`Unsupported platform for stow binary download: ${process.platform}/${process.arch}`);
    }
    const ext = process.platform === "win32" ? ".exe" : "";
    return `stow-${platform}-${arch}${ext}`;
}
function githubToken() {
    return (process.env.STOW_GITHUB_TOKEN?.trim() ||
        process.env.GH_TOKEN?.trim() ||
        process.env.GITHUB_TOKEN?.trim() ||
        process.env.NODE_AUTH_TOKEN?.trim() ||
        undefined);
}
/**
 * Ensure a stow binary exists under the package `bin/` directory, downloading
 * from the GitHub release when missing. Returns the absolute path.
 */
export async function ensureStowBinary(version = process.env.STOW_VERSION?.trim() || DEFAULT_VERSION) {
    const dest = packageBinaryPath();
    if (existsSync(dest)) {
        return dest;
    }
    const asset = releaseAssetName();
    const url = `https://github.com/${REPO}/releases/download/v${version}/${asset}`;
    const headers = {
        Accept: "application/octet-stream",
        "User-Agent": "@chs/stow",
    };
    const token = githubToken();
    if (token) {
        headers.Authorization = `Bearer ${token}`;
    }
    const response = await fetch(url, { headers, redirect: "follow" });
    if (!response.ok || !response.body) {
        throw new Error(`Failed to download stow binary from ${url} (${response.status} ${response.statusText}). ` +
            `Set STOW_BIN to a local binary, or authenticate with GH_TOKEN / GITHUB_TOKEN for private releases.`);
    }
    mkdirSync(dirname(dest), { recursive: true });
    const file = createWriteStream(dest);
    await pipeline(Readable.fromWeb(response.body), file);
    chmodSync(dest, 0o755);
    return dest;
}
//# sourceMappingURL=ensure-binary.js.map