import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { access } from "node:fs/promises";
import { describe, it } from "node:test";
import { ListBucketsCommand, PutObjectCommand } from "@aws-sdk/client-s3";
import { openStow } from "../dist/session.js";
import { stowBinaryAvailable } from "../dist/bin.js";

// The release gate for session lifecycle: sessions must not leak processes or
// directories, and parallel sessions must not collide. This is what makes
// "disposable" a measured property rather than an intention.
const session = { skip: !stowBinaryAvailable() };

function childProcessCount(): number {
  try {
    return execFileSync("pgrep", ["-P", String(process.pid)], { encoding: "utf8" })
      .split("\n")
      .filter((line) => line.trim().length > 0).length;
  } catch {
    return 0;
  }
}

async function assertRemoved(directories: string[]): Promise<void> {
  for (const directory of directories) {
    await assert.rejects(access(directory), (error: unknown) => {
      assert.equal((error as { code?: string }).code, "ENOENT", `${directory} still exists`);
      return true;
    });
  }
}

describe("session lifecycle", session, () => {
  it("leaks no processes or directories across 100 sequential sessions", { timeout: 300_000 }, async () => {
    const processesBefore = childProcessCount();
    const directories: string[] = [];

    for (let index = 0; index < 100; index += 1) {
      const env = await openStow();
      directories.push(env.dataDir);
      await env.s3.send(
        new PutObjectCommand({ Bucket: env.bucket, Key: `k-${index}`, Body: `value-${index}` }),
      );
      await env.close();
    }

    assert.equal(
      childProcessCount(),
      processesBefore,
      "every server process must be reaped after its session closes",
    );
    // Each session's own directory rather than a snapshot of /tmp, so a
    // concurrently running suite cannot make this flaky.
    await assertRemoved(directories);
  });

  it("gives 100 parallel sessions unique endpoints, buckets, and credentials", { timeout: 300_000 }, async () => {
    const processesBefore = childProcessCount();
    const sessions = await Promise.all(Array.from({ length: 100 }, () => openStow()));
    const directories = sessions.map((env) => env.dataDir);

    try {
      const endpoints = new Set(sessions.map((env) => env.endpoint));
      const buckets = new Set(sessions.map((env) => env.bucket));
      assert.equal(endpoints.size, 100, "each session must get its own port");
      assert.equal(buckets.size, 100, "each session must get its own bucket");
      assert.equal(new Set(directories).size, 100, "each session must get its own directory");

      // Credentials are generated per process, so they must differ too.
      const secrets = new Set(sessions.map((env) => env.handoff().AWS_SECRET_ACCESS_KEY));
      assert.equal(secrets.size, 100, "each session must get its own credentials");

      // Every session must be usable and see only its own bucket.
      for (const env of sessions) {
        const listed = await env.s3.send(new ListBucketsCommand({}));
        assert.deepEqual(listed.Buckets?.map((entry) => entry.Name), [env.bucket]);
      }
    } finally {
      await Promise.all(sessions.map((env) => env.close()));
    }

    assert.equal(childProcessCount(), processesBefore, "no process may survive the parallel run");
    await assertRemoved(directories);
  });
});
