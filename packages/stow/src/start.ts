import { rm } from "node:fs/promises";
import { spawn, type ChildProcess } from "node:child_process";
import type { Readable } from "node:stream";
import { StowBinaryNotFoundError, resolveStowBinary, stowBinaryAvailable } from "./bin.js";
import { parseReadyMessage, READY_PROTOCOL_VERSION, type StowReady } from "./ready.js";
import { createStowInstance, DEFAULT_REGION } from "./instance.js";
import type { StartOptions, StowInstance, StowMode } from "./types.js";

const READY_RE =
  /^STOW_READY endpoint=(\S+) access_key=(\S+) secret_key=(\S+) mode=(\S+)/;

export interface ReadyLine {
  endpoint: string;
  accessKeyId: string;
  secretAccessKey: string;
  mode: StowMode;
}

export function parseReadyLine(line: string): ReadyLine | null {
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

function toReadyLine(message: StowReady): ReadyLine {
  return {
    endpoint: message.endpoint,
    accessKeyId: message.accessKeyId,
    secretAccessKey: message.secretAccessKey,
    mode: message.mode as StowMode,
  };
}

/**
 * A best-effort message for a server that only printed the legacy line. Limits
 * are reported as 0, the protocol's "no limit reported" value, so a caller can
 * tell the difference between "unlimited" and "we do not know".
 */
function legacyMessage(line: ReadyLine): StowReady {
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

interface ReadyResult {
  /** The fields the older instance type needs. */
  line: ReadyLine;
  /** The full protocol message, when it came from the versioned channel. */
  message: StowReady;
}

async function waitForReady(
  child: ChildProcess,
  timeoutMs = 10_000,
): Promise<ReadyResult> {
  const descriptor = child.stdio[READY_FD];
  const readyStream = descriptor && typeof (descriptor as Readable).on === "function"
    ? (descriptor as Readable)
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
      reject(
        new Error(
          `Timed out waiting for STOW_READY line.\nstdout:\n${stdoutBuffer}\nstderr:\n${stderrBuffer}`,
        ),
      );
    }, timeoutMs);

    const onError = (error: Error) => {
      if (settled) {
        cleanup();
        return;
      }
      settled = true;
      cleanup();
      reject(error);
    };

    const onExit = (code: number | null, signal: NodeJS.Signals | null) => {
      if (settled) {
        cleanup();
        return;
      }
      settled = true;
      cleanup();
      reject(
        new Error(
          `stow exited before STOW_READY (code=${code ?? "null"}, signal=${signal ?? "null"}).\nstdout:\n${stdoutBuffer}\nstderr:\n${stderrBuffer}`,
        ),
      );
    };

    const onReadyData = (chunk: Buffer) => {
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

    const onStdout = (chunk: Buffer) => {
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

    const onStderr = (chunk: Buffer) => {
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

async function stopChild(child: ChildProcess): Promise<void> {
  if (
    child.exitCode !== null ||
    child.signalCode !== null ||
    child.pid === undefined
  ) {
    return;
  }

  await new Promise<void>((resolve, reject) => {
    let settled = false;
    function finish(error?: Error): void {
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

    function onExit(): void {
      finish();
    }

    function onClose(): void {
      finish();
    }

    function onError(error: Error): void {
      finish(error);
    }

    const forceTimer = setTimeout(() => {
      try {
        if (!child.kill("SIGKILL")) {
          finish(new Error("stow child could not be force-stopped"));
        }
      } catch (error) {
        finish(error instanceof Error ? error : new Error(String(error)));
      }
    }, STOP_GRACE_PERIOD_MS);
    const deadlineTimer = setTimeout(() => {
      try {
        child.kill("SIGKILL");
      } catch {
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
    } catch (error) {
      finish(error instanceof Error ? error : new Error(String(error)));
    }
  });
}

function createStopProcess(child: ChildProcess): () => Promise<void> {
  let stopPromise: Promise<void> | undefined;
  return () => {
    stopPromise ??= stopChild(child);
    return stopPromise;
  };
}

async function withTimeout<T>(
  operation: Promise<T>,
  timeoutMs: number,
  message: string,
): Promise<T> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  try {
    return await Promise.race([
      operation,
      new Promise<T>((_, reject) => {
        timer = setTimeout(() => reject(new Error(message)), timeoutMs);
      }),
    ]);
  } finally {
    if (timer !== undefined) {
      clearTimeout(timer);
    }
  }
}

function buildServeArgs(
  options: StartOptions,
  dataDir: string,
  port: number,
  host: string,
): string[] {
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

function buildChildEnv(options: StartOptions): NodeJS.ProcessEnv {
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
  }
  if (options.accessKey) {
    childEnv.STOW_LOCAL_ACCESS_KEY_ID = options.accessKey;
  }
  if (options.secretKey) {
    childEnv.STOW_LOCAL_SECRET_ACCESS_KEY = options.secretKey;
  }
  return childEnv;
}

async function createStartupBuckets(
  instance: StowInstance,
  buckets: string[],
  remainingStartupMs: () => number,
): Promise<void> {
  for (const bucket of buckets) {
    await withTimeout(
      instance.createBucket(bucket),
      remainingStartupMs(),
      `Timed out creating bucket ${bucket}`,
    );
  }
}

export async function startStow(options: StartOptions = {}): Promise<StowInstance> {
  return (await startStowWithReady(options)).instance;
}

export interface StowStartup {
  instance: StowInstance;
  /**
   * The full readiness message. A session reports capabilities from this rather
   * than from its own assumptions, so the limit a caller is told about is the
   * limit the server is actually enforcing.
   */
  ready: StowReady;
}

export async function startStowWithReady(options: StartOptions = {}): Promise<StowStartup> {
  const dataDir = options.dataDir ?? ".stow";
  if (
    (options.cacheMaxBytes ?? 0) < 0 ||
    (options.cacheMaxObjects ?? 0) < 0 ||
    (options.cacheTtlSeconds ?? 0) < 0
  ) {
    throw new Error("cache limits must not be negative");
  }
  const port = options.port ?? 0;
  const host = options.host ?? "127.0.0.1";

  if (options.cleanSlate) {
    await rm(dataDir, { recursive: true, force: true });
  }

  const startupDeadline = Date.now() + STARTUP_TIMEOUT_MS;
  const remainingStartupMs = (): number => Math.max(1, startupDeadline - Date.now());
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
  } catch (error) {
    await stopChild(child);
    throw error;
  }
}
