import assert from "node:assert/strict";
import { access, mkdtemp, readdir, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { describe, it } from "node:test";
import { GetObjectCommand, ListBucketsCommand, PutObjectCommand } from "@aws-sdk/client-s3";
import { openStow, withStow, DEFAULT_SESSION_MAX_BYTES } from "../dist/session.js";
import { stowBinaryAvailable } from "../dist/bin.js";
import { buildChildEnv, SESSION_GOGC } from "../dist/start.js";

const session = { skip: !stowBinaryAvailable() };

// These do not spawn anything, so they run even where no binary is available.
describe("the child environment of a session's server", () => {
  const withGogc = (value: string | undefined, run: () => void): void => {
    const original = process.env.GOGC;
    if (value === undefined) {
      delete process.env.GOGC;
    } else {
      process.env.GOGC = value;
    }
    try {
      run();
    } finally {
      if (original === undefined) {
        delete process.env.GOGC;
      } else {
        process.env.GOGC = original;
      }
    }
  };

  it("runs a session's server with a tighter collector target than the Go default", () => {
    withGogc(undefined, () => {
      const env = buildChildEnv({ isolatedEnvironment: true });
      assert.equal(env.GOGC, SESSION_GOGC);
      assert.notEqual(SESSION_GOGC, "100", "a session that matches the Go default is not tuning anything");
    });
  });

  it("keeps a collector target the caller set themselves", () => {
    withGogc("400", () => {
      assert.equal(buildChildEnv({ isolatedEnvironment: true }).GOGC, "400");
    });
  });

  it("leaves a long-lived server's collector alone", () => {
    withGogc(undefined, () => {
      const env = buildChildEnv({});
      assert.equal(env.GOGC, undefined);
    });
    withGogc("400", () => {
      assert.equal(buildChildEnv({}).GOGC, "400", "an unset default must not erase the inherited value");
    });
  });

  it("still strips ambient cloud configuration from a session", () => {
    process.env.S3_ENDPOINT_URL = "https://elsewhere.example";
    try {
      const env = buildChildEnv({ isolatedEnvironment: true });
      assert.equal(env.S3_ENDPOINT_URL, undefined);
      assert.equal(env.GOGC, SESSION_GOGC, "stripping must not take the collector target with it");
    } finally {
      delete process.env.S3_ENDPOINT_URL;
    }
  });
});

describe("stow session", session, () => {
  it("runs a round trip in one callback", async () => {
    const seen = await withStow(async (env) => {
      await env.s3.send(
        new PutObjectCommand({ Bucket: env.bucket, Key: "input.json", Body: '{"task":"summarize"}' }),
      );
      const fetched = await env.s3.send(
        new GetObjectCommand({ Bucket: env.bucket, Key: "input.json" }),
      );
      return fetched.Body.transformToString();
    });
    assert.equal(seen, '{"task":"summarize"}');
  });

  it("creates a generated bucket and a local endpoint before the callback runs", async () => {
    await withStow(async (env) => {
      assert.match(env.bucket, /^stow-session-[0-9a-f]{16}$/);
      assert.match(env.endpoint, /^http:\/\/127\.0\.0\.1:\d+$/);
      const buckets = await env.s3.send(new ListBucketsCommand({}));
      assert.deepEqual(
        buckets.Buckets?.map((entry) => entry.Name),
        [env.bucket],
      );
    });
  });

  it("reports capabilities from the server rather than from local assumptions", async () => {
    await withStow(async (env) => {
      const capabilities = env.capabilities();
      assert.equal(capabilities.backend, "memory");
      assert.equal(capabilities.persistent, false);
      assert.equal(capabilities.upstream, false);
      assert.equal(capabilities.protocolVersion, 1);
      // A real binary version, not a placeholder, proves the ready message was
      // parsed rather than reconstructed.
      assert.match(capabilities.binaryVersion, /^\d+\.\d+\.\d+$/);
      assert.equal(capabilities.maxBytes, DEFAULT_SESSION_MAX_BYTES);
      assert.equal(capabilities.maxObjects, 1_000);
      assert.ok(capabilities.maxRequestBytes > 0);
    });
  });

  it("enforces the session byte quota", async () => {
    await withStow(
      async (env) => {
        await assert.rejects(
          env.s3.send(
            new PutObjectCommand({ Bucket: env.bucket, Key: "big", Body: "x".repeat(64) }),
          ),
          (error: unknown) => {
            assert.equal((error as { name?: string }).name, "InsufficientStorage");
            return true;
          },
        );
      },
      { maxBytes: 32 },
    );
  });

  it("hands a child process a fresh environment without mutating the parent", async () => {
    const before = { ...process.env };
    await withStow(async (env) => {
      const first = env.handoff();
      const second = env.handoff();
      assert.notEqual(first, second);
      assert.equal(first.STOW_BUCKET, env.bucket);
      assert.equal(first.AWS_ENDPOINT_URL, env.endpoint);
      assert.ok(first.AWS_ACCESS_KEY_ID);
      assert.ok(first.AWS_SECRET_ACCESS_KEY);
      // Nothing beyond the documented keys, so a handoff cannot leak ambient
      // configuration into a child.
      assert.deepEqual(Object.keys(first).sort(), [
        "AWS_ACCESS_KEY_ID",
        "AWS_ENDPOINT_URL",
        "AWS_REGION",
        "AWS_SECRET_ACCESS_KEY",
        "STOW_BUCKET",
      ]);
    });
    assert.deepEqual({ ...process.env }, before);
  });

  it("closes once, is idempotent, and rejects later use", async () => {
    const env = await openStow();
    const first = env.close();
    const second = env.close();
    assert.equal(first, second, "repeated close must return the same promise");
    await first;
    assert.throws(() => env.capabilities(), (error: unknown) => {
      assert.equal((error as { code?: string }).code, "closed");
      return true;
    });
    assert.throws(() => env.handoff(), /closed/);
  });

  it("rejects the callback error and still cleans up", async () => {
    const failure = new Error("callback exploded");
    await assert.rejects(
      withStow(async () => {
        throw failure;
      }),
      (error: unknown) => {
        assert.equal(error, failure, "the callback's own error must survive cleanup");
        return true;
      },
    );
  });

  it("removes the session directory it created", async () => {
    let dataDir = "";
    await withStow(async (env) => {
      dataDir = env.dataDir;
      const entries = await readdir(dataDir);
      assert.ok(Array.isArray(entries), "the directory exists while the session is open");
    });
    // Asserting on this session's own directory rather than a snapshot of /tmp
    // keeps the check exact while other suites run sessions concurrently.
    await assert.rejects(access(dataDir), (error: unknown) => {
      assert.equal((error as { code?: string }).code, "ENOENT");
      return true;
    });
  });

  it("keeps a caller-supplied directory", async () => {
    const supplied = await mkdtemp(join(tmpdir(), "stow-owned-"));
    try {
      await withStow(
        async (env) => {
          assert.equal(env.dataDir, supplied);
        },
        { dataDir: supplied },
      );
      // Cleanup must never remove a directory the caller owns.
      assert.ok(Array.isArray(await readdir(supplied)));
    } finally {
      await rm(supplied, { recursive: true, force: true });
    }
  });

  it("rejects nonsensical limits before starting anything", async () => {
    for (const options of [{ maxBytes: 0 }, { maxBytes: -1 }, { maxObjects: 0 }, { maxObjects: 1.5 }]) {
      await assert.rejects(openStow(options), /must be a positive integer/);
    }
  });
});
