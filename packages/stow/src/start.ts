import { rm } from "node:fs/promises";
import { spawn, type ChildProcess } from "node:child_process";
import { resolveStowBinary } from "./bin.js";
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

async function waitForReady(
  child: ChildProcess,
  timeoutMs = 10_000,
): Promise<ReadyLine> {
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

    const onStdout = (chunk: Buffer) => {
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
  if (options.allowLiveWrites) {
    args.push("--allow-live-writes");
  }
  return args;
}

function buildChildEnv(options: StartOptions): NodeJS.ProcessEnv {
  const childEnv = { ...process.env };
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
  const remainingStartupMs = (): number => Math.max(1, startupDeadline - Date.now());
  const child = spawn(resolveStowBinary(), buildServeArgs(options, dataDir, port, host), {
    stdio: ["ignore", "pipe", "pipe"],
    env: buildChildEnv(options),
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
    await createStartupBuckets(instance, options.buckets ?? [], remainingStartupMs);
    return instance;
  } catch (error) {
    await stopChild(child);
    throw error;
  }
}
