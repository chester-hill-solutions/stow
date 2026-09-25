#!/usr/bin/env node
// Measure the session lifecycle so the plan's performance targets can be set
// from data rather than assumed.
//
//   node scripts/benchmark-session.mjs [--sessions N] [--payload-bytes N] [--json path]
//
// This is a measurement tool, not a gate. It is deliberately not wired into
// `make standards`, because wall-clock thresholds belong in the plan and should
// be adopted only after a baseline exists and a named machine is recorded.
import { execFileSync } from "node:child_process";
import { mkdirSync, writeFileSync } from "node:fs";
import { cpus, totalmem, platform, arch, release } from "node:os";
import { dirname, resolve } from "node:path";
import { performance } from "node:perf_hooks";

const root = resolve(import.meta.dirname, "..");
const repoRoot = resolve(root, "..", "..");

function argument(name, fallback) {
  const index = process.argv.indexOf(`--${name}`);
  return index === -1 ? fallback : process.argv[index + 1];
}

const SESSIONS = Number(argument("sessions", "20"));
const PAYLOAD_BYTES = Number(argument("payload-bytes", String(1 << 20)));
const JSON_OUT = argument("json", "docs/benchmarks/session-baseline.json");
const SWEEP = process.argv.includes("--sweep");
const SWEEP_MIB = [0, 1, 2, 4, 8];

// The built client beside this script is the only supported way to start a
// managed session, so the benchmark measures what a user measures.
const { Stow } = await import(resolve(root, "dist/index.js"));
const { S3Client, CreateBucketCommand, PutObjectCommand, GetObjectCommand } = await import(
  "@aws-sdk/client-s3"
);

function childPids(parent) {
  try {
    const output = execFileSync("pgrep", ["-P", String(parent)], { encoding: "utf8" });
    return output.split("\n").map((line) => Number(line.trim())).filter((pid) => Number.isInteger(pid) && pid > 0);
  } catch {
    return [];
  }
}

function rssKb(pid) {
  try {
    return Number(execFileSync("ps", ["-o", "rss=", "-p", String(pid)], { encoding: "utf8" }).trim());
  } catch {
    return 0;
  }
}

function percentile(values, fraction) {
  if (values.length === 0) return 0;
  const sorted = [...values].sort((left, right) => left - right);
  const index = Math.min(sorted.length - 1, Math.max(0, Math.ceil(fraction * sorted.length) - 1));
  return sorted[index];
}

function summarize(values) {
  return {
    samples: values.length,
    min: round(Math.min(...values)),
    p50: round(percentile(values, 0.5)),
    p95: round(percentile(values, 0.95)),
    max: round(Math.max(...values)),
  };
}

function round(value) {
  return Math.round(value * 100) / 100;
}

async function runSession(payloadBytes) {
  const payload = Buffer.alloc(payloadBytes, 0x61);
  const started = performance.now();

  const instance = await Stow.start({ backend: "memory" });
  const ready = performance.now();

  const client = new S3Client(instance.awsSdkV3Config());
  const bucket = `bench-${process.pid}-${Math.random().toString(36).slice(2, 8)}`;
  await client.send(new CreateBucketCommand({ Bucket: bucket }));
  const bucketReady = performance.now();
  try {
    await client.send(
      new PutObjectCommand({ Bucket: bucket, Key: "payload.bin", Body: payload }),
    );
  } catch (error) {
    if (error?.name === "EntityTooLarge") {
      throw new Error(
        `payload of ${payloadBytes} bytes exceeds the per-request body limit. ` +
          "Use --sweep, which stays under the limit, or raise the server limit " +
          "deliberately rather than assuming an unbounded request.",
      );
    }
    throw error;
  }
  const putDone = performance.now();
  const fetched = await client.send(new GetObjectCommand({ Bucket: bucket, Key: "payload.bin" }));
  const body = await fetched.Body.transformToByteArray();
  if (body.length !== payloadBytes) {
    throw new Error(`round trip returned ${body.length} bytes, want ${payloadBytes}`);
  }
  const firstOperation = performance.now();

  // Sample the server child's resident set while it holds the payload.
  let peakRssKb = 0;
  for (const pid of childPids(process.pid)) {
    peakRssKb = Math.max(peakRssKb, rssKb(pid));
  }

  await instance.stop();
  const stopped = performance.now();

  return {
    readyMs: ready - started,
    bucketMs: bucketReady - ready,
    putMs: putDone - bucketReady,
    firstOperationMs: firstOperation - bucketReady,
    totalToFirstOperationMs: firstOperation - started,
    shutdownMs: stopped - firstOperation,
    peakRssKb,
  };
}

async function runSeries(payloadBytes, sessions) {
  const results = [];
  for (let index = 0; index < sessions; index += 1) {
    results.push(await runSession(payloadBytes));
    process.stdout.write(`\r${payloadBytes} bytes: session ${index + 1}/${sessions}`);
  }
  process.stdout.write("\n");
  return results;
}

if (SWEEP) {
  // A session's memory cost is a fixed process overhead plus a per-byte cost
  // that is larger than one, because the v1 write path buffers the body and the
  // runtime copies it again. The slope is what a session quota has to be sized
  // against, so measure it rather than assume a multiplier.
  const points = [];
  for (const mib of SWEEP_MIB) {
    const results = await runSeries(mib * 1048576, Math.max(3, Math.min(SESSIONS, 10)));
    points.push({
      payloadBytes: mib * 1048576,
      peakRssKbP50: percentile(results.map((r) => r.peakRssKb), 0.5),
      readyMsP50: round(percentile(results.map((r) => r.readyMs), 0.5)),
    });
  }
  const first = points[0];
  const last = points[points.length - 1];
  const payloadDeltaMiB = (last.payloadBytes - first.payloadBytes) / 1048576;
  const rssDeltaMiB = (last.peakRssKbP50 - first.peakRssKbP50) / 1024;
  const baselineRssMiB = first.peakRssKbP50 / 1024;
  const report = {
    environment: {
      measuredAt: new Date().toISOString(),
      node: process.version,
      os: `${platform()} ${arch()} ${release()}`,
      cpu: cpus()[0]?.model ?? "unknown",
      cpuCount: cpus().length,
      totalMemoryMb: Math.round(totalmem() / 1048576),
      sessionsPerPoint: Math.max(3, Math.min(SESSIONS, 10)),
      note: "Peak RSS is sampled from the server child process while it holds the payload.",
    },
    memorySweep: points,
    derived: {
      baselineRssMiB: round(baselineRssMiB),
      rssMiBPerPayloadMiB: round(rssDeltaMiB / payloadDeltaMiB),
      note: "rssMiBPerPayloadMiB is the multiplier a session byte quota must be multiplied by to predict peak RSS.",
    },
  };
  const output = resolve(repoRoot, JSON_OUT);
  mkdirSync(dirname(output), { recursive: true });
  writeFileSync(output, `${JSON.stringify(report, null, 2)}\n`);
  console.log(JSON.stringify(report, null, 2));
  console.log(`\nwrote ${JSON_OUT}`);
  process.exit(0);
}

const results = [];
for (let index = 0; index < SESSIONS; index += 1) {
  results.push(await runSession(PAYLOAD_BYTES));
  process.stdout.write(`session ${index + 1}/${SESSIONS}\r`);
}
process.stdout.write("\n");

const environment = {
  measuredAt: new Date().toISOString(),
  node: process.version,
  os: `${platform()} ${arch()} ${release()}`,
  cpu: cpus()[0]?.model ?? "unknown",
  cpuCount: cpus().length,
  totalMemoryMb: Math.round(totalmem() / 1048576),
  payloadBytes: PAYLOAD_BYTES,
  sessions: SESSIONS,
};

const report = {
  environment,
  readyMs: summarize(results.map((r) => r.readyMs)),
  firstOperationMs: summarize(results.map((r) => r.firstOperationMs)),
  shutdownMs: summarize(results.map((r) => r.shutdownMs)),
  totalToFirstOperationMs: summarize(results.map((r) => r.totalToFirstOperationMs)),
  peakRssKb: summarize(results.map((r) => r.peakRssKb)),
};

const output = resolve(repoRoot, JSON_OUT);
mkdirSync(dirname(output), { recursive: true });
writeFileSync(output, `${JSON.stringify(report, null, 2)}\n`);

console.log(JSON.stringify(report, null, 2));
console.log(`\nwrote ${JSON_OUT}`);
