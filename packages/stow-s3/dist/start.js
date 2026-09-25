import { spawn } from "node:child_process";
import { StowBinaryNotFoundError, resolveStowBinary, stowBinaryAvailable } from "./bin.js";
import { resetOwnedData } from "./ownership.js";
import { waitForReady, READY_FD } from "./ready-reader.js";
import { StowProtocolError } from "./ready.js";
import { createStowInstance } from "./instance.js";
// The legacy readiness line lives with the rest of the protocol reader. It is
// re-exported here because it was part of this module's public surface before
// the reader was split out, and moving a symbol is not a reason to break an
// import path.
export { parseReadyLine } from "./ready-reader.js";
/**
 * The Go collector target a session's own server runs with.
 *
 * A session is short-lived and one of many on the machine, so its peak memory is
 * what matters and its throughput rarely is. The Python client must use the same
 * value; check-version.mjs fails if the two drift.
 */
export const SESSION_GOGC = "50";
const STARTUP_TIMEOUT_MS = 10_000;
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
    if (options.region) {
        args.push("--region", options.region);
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
function assertUsableStartupLimits(options) {
    if ((options.cacheMaxBytes ?? 0) < 0 ||
        (options.cacheMaxObjects ?? 0) < 0 ||
        (options.cacheTtlSeconds ?? 0) < 0) {
        throw new Error("cache limits must not be negative");
    }
}
// Resolving the binary is separated from spawning it so a missing binary fails
// with an actionable message instead of a bare ENOENT for the literal "stow".
function resolveStartableBinary() {
    const binary = resolveStowBinary();
    if (!stowBinaryAvailable()) {
        throw new StowBinaryNotFoundError(binary);
    }
    return binary;
}
export async function startStowWithReady(options = {}) {
    assertUsableStartupLimits(options);
    const dataDir = options.dataDir ?? ".stow";
    const port = options.port ?? 0;
    const host = options.host ?? "127.0.0.1";
    await resetDataDirIfRequested(options, dataDir);
    if (options.signal?.aborted) {
        throw new StowProtocolError("cancelled", "cancelled before the server was started");
    }
    const startupDeadline = Date.now() + (options.timeoutMs ?? STARTUP_TIMEOUT_MS);
    const remainingStartupMs = () => Math.max(1, startupDeadline - Date.now());
    const binary = resolveStartableBinary();
    const child = spawn(binary, buildServeArgs(options, dataDir, port, host), {
        // The fourth entry is the readiness descriptor the server writes to.
        stdio: ["ignore", "pipe", "pipe", "pipe"],
        env: buildChildEnv(options),
    });
    // An abort during startup must not leave a child running. waitForReady owns
    // the descriptor, but killing the process is the caller's cancellation to
    // enforce and has to happen whichever stage is in flight.
    const onAbort = () => {
        void stopChild(child);
    };
    options.signal?.addEventListener("abort", onAbort, { once: true });
    try {
        const ready = await waitForReady(child, remainingStartupMs(), options.signal);
        const instance = createStowInstance({
            endpoint: ready.line.endpoint,
            accessKeyId: ready.line.accessKeyId,
            secretAccessKey: ready.line.secretAccessKey,
            // The server reports its own region. Forcing a constant here discarded
            // the field the readiness protocol went to the trouble of sending, and
            // produced signatures for the wrong region against a non-default server.
            region: ready.message.region,
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
    finally {
        options.signal?.removeEventListener("abort", onAbort);
    }
}
//# sourceMappingURL=start.js.map