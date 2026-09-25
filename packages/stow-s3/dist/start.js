import { spawn } from "node:child_process";
import { StowBinaryNotFoundError, resolveStowBinary, stowBinaryAvailable } from "./bin.js";
import { resetOwnedData } from "./ownership.js";
import { parseReadyMessage, READY_PROTOCOL_VERSION } from "./ready.js";
import { createStowInstance, DEFAULT_REGION } from "./instance.js";
const READY_RE = /^STOW_READY endpoint=(\S+) access_key=(\S+) secret_key=(\S+) mode=(\S+)/;
/**
 * The Go collector target a session's own server runs with.
 *
 * A session is short-lived and one of many on the machine, so its peak memory is
 * what matters and its throughput rarely is. The Python client must use the same
 * value; check-version.mjs fails if the two drift.
 */
export const SESSION_GOGC = "50";
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
// Descriptor the server writes the versioned readiness object to. Index 3 is the
// first entry after stdin, stdout, and stderr.
const READY_FD = 3;
function toReadyLine(message) {
    return {
        endpoint: message.endpoint,
        accessKeyId: message.accessKeyId,
        secretAccessKey: message.secretAccessKey,
        mode: message.mode,
    };
}
/**
 * A best-effort message for a server that only printed the legacy line. Limits
 * are reported as 0, the protocol's "no limit reported" value, so a caller can
 * tell the difference between "unlimited" and "we do not know".
 */
function legacyMessage(line) {
    return {
        protocolVersion: READY_PROTOCOL_VERSION,
        binaryVersion: "unknown",
        endpoint: line.endpoint,
        region: DEFAULT_REGION,
        accessKeyId: line.accessKeyId,
        secretAccessKey: line.secretAccessKey,
        mode: line.mode,
        backend: "unknown",
        capabilities: {
            persistent: false,
            multipart: false,
            upstream: false,
            conditionalWrites: false,
            presignedUrls: false,
            maxBytes: 0,
            maxObjects: 0,
            maxRequestBytes: 0,
        },
    };
}
async function waitForReady(child, timeoutMs = 10_000) {
    const descriptor = child.stdio[READY_FD];
    const readyStream = descriptor && typeof descriptor.on === "function"
        ? descriptor
        : undefined;
    return new Promise((resolve, reject) => {
        let stdoutBuffer = "";
        let stderrBuffer = "";
        let readyBuffer = "";
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
        const onReadyData = (chunk) => {
            if (settled) {
                return;
            }
            readyBuffer = (readyBuffer + chunk.toString("utf8")).slice(-MAX_DIAGNOSTIC_BYTES);
            for (const line of readyBuffer.split(/\r?\n/)) {
                if (line.trim().length === 0) {
                    continue;
                }
                if (settled) {
                    return;
                }
                settled = true;
                cleanup(false);
                const message = parseReadyMessage(line);
                resolve({ line: toReadyLine(message), message });
                return;
            }
        };
        const onStdout = (chunk) => {
            if (settled) {
                return;
            }
            stdoutBuffer = (stdoutBuffer + chunk.toString("utf8")).slice(-MAX_DIAGNOSTIC_BYTES);
            // Fallback for a server that ignores --ready-fd and writes the legacy
            // line instead. Capabilities are unknown in that case and reported as
            // such rather than invented.
            for (const line of stdoutBuffer.split(/\r?\n/)) {
                const legacy = parseReadyLine(line);
                if (!legacy) {
                    continue;
                }
                if (settled) {
                    return;
                }
                settled = true;
                cleanup(false);
                resolve({ line: legacy, message: legacyMessage(legacy) });
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
                readyStream?.off("data", onReadyData);
            }
            child.off("error", onError);
            child.off("exit", onExit);
        };
        child.stdout?.on("data", onStdout);
        child.stderr?.on("data", onStderr);
        readyStream?.on("data", onReadyData);
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
function buildServeArgs(options, dataDir, port, host) {
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
    if (options.cacheTtlSeconds !== undefined) {
        args.push("--cache-ttl", `${options.cacheTtlSeconds}s`);
    }
    if (options.allowLiveWrites) {
        args.push("--allow-live-writes");
    }
    if (options.maxBytes !== undefined) {
        args.push("--max-bytes", String(options.maxBytes));
    }
    if (options.maxObjects !== undefined) {
        args.push("--max-objects", String(options.maxObjects));
    }
    // Ask for the versioned readiness channel. The server then keeps credentials
    // off stdout and writes them to this descriptor instead.
    args.push("--ready-fd", String(READY_FD));
    if (options.parentPid) {
        // Opt-in parent-death watch. The server exits if this process disappears,
        // even if it is killed rather than closed cleanly.
        args.push("--parent-pid", String(options.parentPid));
    }
    return args;
}
export function buildChildEnv(options) {
    const childEnv = { ...process.env };
    if (options.isolatedEnvironment) {
        // A scoped session must not inherit cloud configuration from the parent
        // shell. STOW_*, S3_*, and AWS_* are stripped before any session-specific
        // values are set below, so a stray credential cannot turn a local session
        // into a run-through one.
        for (const key of Object.keys(childEnv)) {
            if (/^(STOW_|S3_|AWS_)/.test(key)) {
                delete childEnv[key];
            }
        }
        // A session is an ephemeral local test fixture that several may run at once,
        // so its peak memory matters more than its throughput. Measured on 4 MiB
        // puts, a session's peak RSS per MiB of payload falls from 4.19 to 3.27 at
        // GOGC=50, and to 2.96 at GOGC=20, at roughly 6% and 45% more put latency
        // respectively. 50 takes most of the memory for a fraction of the cost.
        //
        // A caller who set GOGC themselves keeps their value: an explicit choice in
        // the environment outranks a default. A long-lived server never reaches this
        // branch, so its collector is left alone.
        if (childEnv.GOGC === undefined) {
            childEnv.GOGC = SESSION_GOGC;
        }
    }
    if (options.accessKey) {
        childEnv.STOW_LOCAL_ACCESS_KEY_ID = options.accessKey;
    }
    if (options.secretKey) {
        childEnv.STOW_LOCAL_SECRET_ACCESS_KEY = options.secretKey;
    }
    return childEnv;
}
async function createStartupBuckets(instance, buckets, remainingStartupMs) {
    for (const bucket of buckets) {
        await withTimeout(instance.createBucket(bucket), remainingStartupMs(), `Timed out creating bucket ${bucket}`);
    }
}
export async function startStow(options = {}) {
    return (await startStowWithReady(options)).instance;
}
// Both spellings mean the same thing and are now equally guarded: only a
// directory stow created is deleted. cleanSlate is kept as an alias because
// it shipped in 0.2.x, but it is no longer a raw recursive delete of whatever
// string the caller passed.
async function resetDataDirIfRequested(options, dataDir) {
    if (options.resetOwnedData || options.cleanSlate) {
        await resetOwnedData(dataDir);
    }
}
export async function startStowWithReady(options = {}) {
    const dataDir = options.dataDir ?? ".stow";
    if ((options.cacheMaxBytes ?? 0) < 0 ||
        (options.cacheMaxObjects ?? 0) < 0 ||
        (options.cacheTtlSeconds ?? 0) < 0) {
        throw new Error("cache limits must not be negative");
    }
    const port = options.port ?? 0;
    const host = options.host ?? "127.0.0.1";
    await resetDataDirIfRequested(options, dataDir);
    const startupDeadline = Date.now() + STARTUP_TIMEOUT_MS;
    const remainingStartupMs = () => Math.max(1, startupDeadline - Date.now());
    // Fail with an actionable error before spawn turns a missing binary into a
    // bare ENOENT for the literal string "stow".
    const binary = resolveStowBinary();
    if (!stowBinaryAvailable()) {
        throw new StowBinaryNotFoundError(binary);
    }
    const child = spawn(binary, buildServeArgs(options, dataDir, port, host), {
        // The fourth entry is the readiness descriptor the server writes to.
        stdio: ["ignore", "pipe", "pipe", "pipe"],
        env: buildChildEnv(options),
    });
    try {
        const ready = await waitForReady(child, remainingStartupMs());
        const instance = createStowInstance({
            endpoint: ready.line.endpoint,
            accessKeyId: ready.line.accessKeyId,
            secretAccessKey: ready.line.secretAccessKey,
            region: DEFAULT_REGION,
            mode: ready.line.mode,
            dataDir,
            stopProcess: createStopProcess(child),
        });
        await createStartupBuckets(instance, options.buckets ?? [], remainingStartupMs);
        return { instance, ready: ready.message };
    }
    catch (error) {
        await stopChild(child);
        throw error;
    }
}
//# sourceMappingURL=start.js.map