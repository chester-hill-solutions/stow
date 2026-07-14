import { rm } from "node:fs/promises";
import { spawn, type ChildProcess } from "node:child_process";
import { resolveStowBinary } from "./bin.js";
import { ensureStowBinary } from "./ensure-binary.js";
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
        return;
      }
      settled = true;
      cleanup();
      reject(error);
    };

    const onExit = (code: number | null, signal: NodeJS.Signals | null) => {
      if (settled) {
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
      stdoutBuffer += chunk.toString("utf8");
      for (const line of stdoutBuffer.split(/\r?\n/)) {
        const ready = parseReadyLine(line);
        if (!ready) {
          continue;
        }
        if (settled) {
          return;
        }
        settled = true;
        cleanup();
        resolve(ready);
        return;
      }
    };

    const onStderr = (chunk: Buffer) => {
      stderrBuffer += chunk.toString("utf8");
    };

    const cleanup = () => {
      clearTimeout(timer);
      child.stdout?.off("data", onStdout);
      child.stderr?.off("data", onStderr);
      child.off("error", onError);
      child.off("exit", onExit);
    };

    child.stdout?.on("data", onStdout);
    child.stderr?.on("data", onStderr);
    child.on("error", onError);
    child.on("exit", onExit);
  });
}

async function stopChild(child: ChildProcess): Promise<void> {
  if (child.exitCode !== null || child.signalCode !== null) {
    return;
  }

  await new Promise<void>((resolve) => {
    const timer = setTimeout(() => {
      child.kill("SIGKILL");
    }, 5_000);

    child.once("exit", () => {
      clearTimeout(timer);
      resolve();
    });

    child.kill("SIGTERM");
  });
}

export async function startStow(options: StartOptions = {}): Promise<StowInstance> {
  const dataDir = options.dataDir ?? ".stow";
  const port = options.port ?? 0;
  const host = options.host ?? "127.0.0.1";

  if (options.cleanSlate) {
    await rm(dataDir, { recursive: true, force: true });
  }

  let binary = resolveStowBinary();
  if (!process.env.STOW_BIN?.trim() && binary === "stow") {
    binary = await ensureStowBinary();
  }
  const args = ["serve", "--port", String(port), "--data-dir", dataDir, "--host", host];
  if (options.mode) {
    args.push("--mode", options.mode);
  }
  if (options.cacheDir) {
    args.push("--cache-dir", options.cacheDir);
  }
  if (options.allowLiveWrites) {
    args.push("--allow-live-writes");
  }
  if (options.accessKey) {
    args.push("--access-key", options.accessKey);
  }
  if (options.secretKey) {
    args.push("--secret-key", options.secretKey);
  }

  const child = spawn(binary, args, {
    stdio: ["ignore", "pipe", "pipe"],
    env: process.env,
  });

  let ready: ReadyLine;
  try {
    ready = await waitForReady(child);
  } catch (error) {
    await stopChild(child);
    throw error;
  }

  const instance = createStowInstance({
    endpoint: ready.endpoint,
    accessKeyId: ready.accessKeyId,
    secretAccessKey: ready.secretAccessKey,
    region: DEFAULT_REGION,
    mode: ready.mode,
    dataDir,
    stopProcess: async () => {
      await stopChild(child);
    },
  });

  for (const bucket of options.buckets ?? []) {
    await instance.createBucket(bucket);
  }

  return instance;
}
