import type { ChildProcess } from "node:child_process";
import type { Readable } from "node:stream";

import { takeCompleteRecords } from "./ready-framing.js";
import { parseReadyMessage, READY_PROTOCOL_VERSION, type StowReady, StowProtocolError } from "./ready.js";
import { DEFAULT_REGION } from "./instance.js";
import type { StowMode } from "./types.js";

const READY_RE =
  /^STOW_READY endpoint=(\S+) access_key=(\S+) secret_key=(\S+) mode=(\S+)/;

export interface ReadyLine {
  endpoint: string;
  accessKeyId: string;
  secretAccessKey: string;
  mode: StowMode;
}

/**
 * Parse the pre-protocol single-line readiness announcement.
 *
 * Still read, because a server that ignores --ready-fd and writes the legacy
 * line to stdout is a server a client can still talk to. Its capabilities are
 * genuinely unknown in that case and are reported as unknown rather than
 * invented.
 */
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

/**
 * Reading the server's readiness message.
 *
 * Separate from spawning the server so each file has one job: this one
 * implements the wire contract, and start.ts implements the process lifecycle.
 */

const MAX_DIAGNOSTIC_BYTES = 64 * 1024;
// A readiness record is a single JSON object. Anything larger than this without
// a newline is a protocol violation, and is reported as one rather than being
// silently truncated into invalid JSON.
const MAX_READY_RECORD_BYTES = 256 * 1024;
// How long to let a child report its own exit after the readiness pipe closes.
// The pipe closing is a consequence of the exit, not an independent fault, and
// the exit message names the cause.
const READY_END_GRACE_MS = 50;

// Descriptor the server writes the versioned readiness object to. Index 3 is the
// first entry after stdin, stdout, and stderr.
export const READY_FD = 3;

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

export async function waitForReady(
  child: ChildProcess,
  timeoutMs = 10_000,
  signal?: AbortSignal,
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

    const onAbort = () => {
      if (settled) {
        cleanup();
        return;
      }
      settled = true;
      cleanup();
      reject(new StowProtocolError("cancelled", "cancelled while waiting for the server to become ready"));
    };

    if (signal?.aborted) {
      onAbort();
      return;
    }
    signal?.addEventListener("abort", onAbort, { once: true });

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

    const fail = (error: Error) => {
      if (settled) {
        return;
      }
      settled = true;
      cleanup();
      reject(error);
    };

    // A JSON ready record is one line. A chunk boundary can fall anywhere, so
    // only a newline-terminated record may be parsed. Parsing the trailing
    // partial element is how a split record used to reach the JSON parser, and
    // a syntax error there rejected nothing.
    const onReadyData = (chunk: Buffer) => {
      if (settled) {
        return;
      }
      readyBuffer += chunk.toString("utf8");
      const framed = takeCompleteRecords(readyBuffer);
      readyBuffer = framed.rest;
      if (readyBuffer.length > MAX_READY_RECORD_BYTES) {
        fail(
          new StowProtocolError(
            "protocol_mismatch",
            `readiness record exceeded ${MAX_READY_RECORD_BYTES} bytes without a newline`,
          ),
        );
        return;
      }
      for (const record of framed.records) {
        if (record.trim().length === 0) {
          continue;
        }
        let message: StowReady;
        try {
          message = parseReadyMessage(record);
        } catch (error) {
          // Reached from a stream callback, so a throw here would escape as an
          // uncaught exception and leave the promise pending forever, because
          // the timeout was already cleared.
          fail(error instanceof Error ? error : new Error(String(error)));
          return;
        }
        settled = true;
        cleanup(false);
        resolve({ line: toReadyLine(message), message });
        return;
      }
    };

    // A readiness pipe that closes before a record arrives is a failure, not a
    // condition to wait out the full timeout for.
    //
    // The descriptor closes as a consequence of the child exiting, so 'end'
    // usually arrives first and would otherwise replace the far more useful
    // exit diagnosis with a pipe one. Wait briefly for the child to report its
    // own exit before concluding the descriptor failed by itself. The grace is
    // short: it only has to span the gap between the pipe closing and the exit
    // event, not a real timeout.
    let endGraceTimer: NodeJS.Timeout | undefined;
    const onReadyEnd = () => {
      endGraceTimer = setTimeout(() => {
        fail(
          new Error(
            `readiness descriptor closed before a complete STOW_READY record arrived.\nstdout:\n${stdoutBuffer}\nstderr:\n${stderrBuffer}`,
          ),
        );
      }, READY_END_GRACE_MS);
    };

    const onReadyError = (error: Error) => {
      fail(
        new Error(
          `readiness descriptor failed: ${error.message}\nstdout:\n${stdoutBuffer}\nstderr:\n${stderrBuffer}`,
        ),
      );
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
      if (endGraceTimer !== undefined) {
        clearTimeout(endGraceTimer);
      }
      signal?.removeEventListener("abort", onAbort);
      if (removeOutput) {
        child.stdout?.off("data", onStdout);
        child.stderr?.off("data", onStderr);
        readyStream?.off("data", onReadyData);
        readyStream?.off("end", onReadyEnd);
        readyStream?.off("error", onReadyError);
      }
      child.off("error", onError);
      child.off("exit", onExit);
    };

    child.stdout?.on("data", onStdout);
    child.stderr?.on("data", onStderr);
    readyStream?.on("data", onReadyData);
    readyStream?.on("end", onReadyEnd);
    readyStream?.on("error", onReadyError);
    child.on("error", onError);
    child.on("exit", onExit);
  });
}
