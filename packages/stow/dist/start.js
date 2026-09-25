import { rm } from "node:fs/promises";
import { spawn } from "node:child_process";
import { resolveStowBinary } from "./bin.js";
import { createStowInstance, DEFAULT_REGION } from "./instance.js";
const READY_RE = /^STOW_READY endpoint=(\S+) access_key=(\S+) secret_key=(\S+) mode=(\S+)/;
export function parseReadyLine(line) {
    const match = line.trim().match(READY_RE);
    if (!match) {
        return null;
    }
    const [, endpoint, accessKeyId, secretAccessKey, mode] = match;
    if (!endpoint || !accessKeyId || !secretAccessKey || !mode) {
        return null;
    }
    return {
        endpoint,
        accessKeyId,
        secretAccessKey,
        mode: mode === "run-through" ? "run-through" : "local",
    };
}
const MAX_DIAGNOSTIC_BYTES = 64 * 1024;
const STARTUP_TIMEOUT_MS = 10_000;
async function waitForReady(child, timeoutMs = 10_000) {
    return new Promise((resolve, reject) => {
        let stdoutBuffer = "";
        let stderrBuffer = "";
        let settled = false;
        const timer = setTimeout(() => {
            if (settled) {
                return;
            }
            settled = true;
            cleanup();
            reject(new Error(`Timed out waiting for STOW_READY line.\nstdout:\n${stdoutBuffer}\nstderr:\n${stderrBuffer}`));
        }, timeoutMs);
        const onError = (error) => {
            if (settled) {
                cleanup();
                return;
            }
            settled = true;
            cleanup();
            reject(error);
        };
        const onExit = (code, signal) => {
            if (settled) {
                cleanup();
                return;
            }
            settled = true;
            cleanup();
            reject(new Error(`stow exited before STOW_READY (code=${code ?? "null"}, signal=${signal ?? "null"}).\nstdout:\n${stdoutBuffer}\nstderr:\n${stderrBuffer}`));
        };
        const onStdout = (chunk) => {
            if (settled) {
                return;
            }
            stdoutBuffer = (stdoutBuffer + chunk.toString("utf8")).slice(-MAX_DIAGNOSTIC_BYTES);
            for (const line of stdoutBuffer.split(/\r?\n/)) {
                const ready = parseReadyLine(line);
                if (!ready) {
                    continue;
                }
                if (settled) {
                    return;
                }
                settled = true;
                cleanup(false);
                resolve(ready);
                return;
            }
        };
        const onStderr = (chunk) => {
            if (settled) {
                return;
            }
            stderrBuffer = (stderrBuffer + chunk.toString("utf8")).slice(-MAX_DIAGNOSTIC_BYTES);
        };
        const cleanup = (removeOutput = true) => {
            clearTimeout(timer);
            if (removeOutput) {
                child.stdout?.off("data", onStdout);
                child.stderr?.off("data", onStderr);
            }
            child.off("error", onError);
            child.off("exit", onExit);
        };
        child.stdout?.on("data", onStdout);
        child.stderr?.on("data", onStderr);
        child.on("error", onError);
        child.on("exit", onExit);
    });
}
const STOP_GRACE_PERIOD_MS = 5_000;
const STOP_WAIT_PERIOD_MS = 10_000;
async function stopChild(child) {
    if (child.exitCode !== null ||
        child.signalCode !== null ||
        child.pid === undefined) {
        return;
    }
    await new Promise((resolve, reject) => {
        let settled = false;
        function finish(error) {
            if (settled) {
                return;
            }
            settled = true;
            clearTimeout(forceTimer);
            clearTimeout(deadlineTimer);
            child.off("exit", onExit);
            child.off("close", onClose);
            child.off("error", onError);
            if (error) {
                reject(error);
                return;
            }
            resolve();
        }
        function onExit() {
            finish();
        }
        function onClose() {
            finish();
        }
        function onError(error) {
            finish(error);
        }
        const forceTimer = setTimeout(() => {
            try {
                if (!child.kill("SIGKILL")) {
                    finish(new Error("stow child could not be force-stopped"));
                }
            }
            catch (error) {
                finish(error instanceof Error ? error : new Error(String(error)));
            }
        }, STOP_GRACE_PERIOD_MS);
        const deadlineTimer = setTimeout(() => {
            try {
                child.kill("SIGKILL");
            }
            catch {
                // The timeout error below is the useful diagnostic for the caller.
            }
            finish(new Error("timed out waiting for stow child to exit"));
        }, STOP_WAIT_PERIOD_MS);
        child.once("exit", onExit);
        child.once("close", onClose);
        child.once("error", onError);
        try {
            if (!child.kill("SIGTERM")) {
                finish(new Error("stow child could not be stopped"));
            }
        }
        catch (error) {
            finish(error instanceof Error ? error : new Error(String(error)));
        }
    });
}
function createStopProcess(child) {
    let stopPromise;
    return () => {
        stopPromise ??= stopChild(child);
        return stopPromise;
    };
}
async function withTimeout(operation, timeoutMs, message) {
    let timer;
    try {
        return await Promise.race([
            operation,
            new Promise((_, reject) => {
                timer = setTimeout(() => reject(new Error(message)), timeoutMs);
            }),
        ]);
    }
    finally {
        if (timer !== undefined) {
            clearTimeout(timer);
        }
    }
}
export async function startStow(options = {}) {
    const dataDir = options.dataDir ?? ".stow";
    if ((options.cacheMaxBytes ?? 0) < 0 || (options.cacheMaxObjects ?? 0) < 0) {
        throw new Error("cache limits must not be negative");
    }
    const port = options.port ?? 0;
    const host = options.host ?? "127.0.0.1";
    if (options.cleanSlate) {
        await rm(dataDir, { recursive: true, force: true });
    }
    const startupDeadline = Date.now() + STARTUP_TIMEOUT_MS;
    const remainingStartupMs = () => Math.max(1, startupDeadline - Date.now());
    const binary = resolveStowBinary();
    const args = ["serve", "--port", String(port), "--data-dir", dataDir, "--host", host];
    if (options.baseHost) {
        args.push("--base-host", options.baseHost);
    }
    if (options.allowPublicAdmin) {
        args.push("--allow-public-admin");
    }
    if (options.backend) {
        args.push("--backend", options.backend);
    }
    if (options.mode) {
        args.push("--mode", options.mode);
    }
    if (options.cacheDir) {
        args.push("--cache-dir", options.cacheDir);
    }
    if (options.cacheMaxBytes !== undefined) {
        args.push("--cache-max-bytes", String(options.cacheMaxBytes));
    }
    if (options.cacheMaxObjects !== undefined) {
        args.push("--cache-max-objects", String(options.cacheMaxObjects));
    }
    if (options.allowLiveWrites) {
        args.push("--allow-live-writes");
    }
    const childEnv = { ...process.env };
    if (options.accessKey) {
        childEnv.STOW_LOCAL_ACCESS_KEY_ID = options.accessKey;
    }
    if (options.secretKey) {
        childEnv.STOW_LOCAL_SECRET_ACCESS_KEY = options.secretKey;
    }
    const child = spawn(binary, args, {
        stdio: ["ignore", "pipe", "pipe"],
        env: childEnv,
    });
    try {
        const ready = await waitForReady(child, remainingStartupMs());
        const instance = createStowInstance({
            endpoint: ready.endpoint,
            accessKeyId: ready.accessKeyId,
            secretAccessKey: ready.secretAccessKey,
            region: DEFAULT_REGION,
            mode: ready.mode,
            dataDir,
            stopProcess: createStopProcess(child),
        });
        for (const bucket of options.buckets ?? []) {
            await withTimeout(instance.createBucket(bucket), remainingStartupMs(), `Timed out creating bucket ${bucket}`);
        }
        return instance;
    }
    catch (error) {
        await stopChild(child);
        throw error;
    }
}
//# sourceMappingURL=start.js.map