import assert from "node:assert/strict";
import { chmod, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { createServer } from "node:http";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { setTimeout as delay } from "node:timers/promises";
import { describe, it } from "node:test";
import { S3Client } from "@aws-sdk/client-s3";
import {
  buildAwsSdkV3Config,
  createStowInstance,
  verifyObjectReadable,
} from "../dist/instance.js";
import { startStow } from "../dist/start.js";
import { Stow } from "../dist/index.js";

interface FakeS3Server {
  endpoint: string;
  requests: string[];
  close(): Promise<void>;
}

function listPage(keys: string[], nextToken?: string): string {
  const contents = keys
    .map(
      (key) =>
        `<Contents><Key>${key}</Key><Size>${key.length}</Size><ETag>"etag-${key}"</ETag><LastModified>2024-01-01T00:00:00.000Z</LastModified></Contents>`,
    )
    .join("");
  return [
    '<?xml version="1.0" encoding="UTF-8"?>',
    '<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">',
    "<Name>bucket</Name>",
    `<IsTruncated>${nextToken ? "true" : "false"}</IsTruncated>`,
    nextToken ? `<NextContinuationToken>${nextToken}</NextContinuationToken>` : "",
    contents,
    "</ListBucketResult>",
  ].join("");
}

async function startFakeS3Server(): Promise<FakeS3Server> {
  const requests: string[] = [];
  const server = createServer((request, response) => {
    const url = new URL(request.url ?? "/", "http://127.0.0.1");
    requests.push(`${request.method} ${url.pathname}${url.search}`);

    if (
      request.method === "GET" &&
      (url.pathname === "/bucket" || url.pathname === "/bucket/") &&
      url.searchParams.get("list-type") === "2"
    ) {
      const page = url.searchParams.has("continuation-token")
        ? listPage(["b"])
        : listPage(["a"], "page-2");
      response.writeHead(200, { "content-type": "application/xml" });
      response.end(page);
      return;
    }
    if (request.method === "DELETE") {
      response.writeHead(204);
      response.end();
      return;
    }
    if (request.method === "PUT") {
      request.resume();
      request.on("end", () => {
        response.writeHead(200);
        response.end();
      });
      return;
    }
    if (request.method === "GET") {
      response.writeHead(200, { "content-type": "text/plain" });
      response.end("fixture body");
      return;
    }
    response.writeHead(500);
    response.end();
  });

  await new Promise<void>((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => {
      server.off("error", reject);
      resolve();
    });
  });
  const address = server.address();
  assert.ok(address !== null && typeof address !== "string");

  return {
    endpoint: `http://127.0.0.1:${address.port}`,
    requests,
    close: () =>
      new Promise<void>((resolve, reject) => {
        server.close((error) => {
          if (error) {
            reject(error);
            return;
          }
          resolve();
        });
      }),
  };
}

function instanceFor(endpoint: string): ReturnType<typeof createStowInstance> {
  return createStowInstance({
    endpoint,
    accessKeyId: "access",
    secretAccessKey: "secret",
    mode: "local",
    dataDir: "unused",
    stopProcess: async () => {},
  });
}

async function assertProcessExited(pid: number): Promise<void> {
  const deadline = Date.now() + 2_000;
  while (Date.now() < deadline) {
    try {
      process.kill(pid, 0);
      await delay(20);
    } catch {
      return;
    }
  }
  assert.fail(`process ${pid} is still running`);
}

function restoreEnvironment(values: Record<string, string | undefined>): void {
  for (const [name, value] of Object.entries(values)) {
    if (value === undefined) {
      delete process.env[name];
    } else {
      process.env[name] = value;
    }
  }
}

describe("startup lifecycle", () => {
  it("cleans up when the child fails before readiness", async () => {
    const directory = await mkdtemp(join(tmpdir(), "stow-readiness-"));
    const previous = process.env.STOW_BIN;
    try {
      process.env.STOW_BIN = join(directory, "missing-stow");
      await assert.rejects(
        startStow({ dataDir: join(directory, "data") }),
        /ENOENT/,
      );
    } finally {
      restoreEnvironment({ STOW_BIN: previous });
      await rm(directory, { recursive: true, force: true });
    }
  });

  it("kills and awaits a child when post-ready initialization fails", async () => {
    if (process.platform === "win32") {
      return;
    }

    const directory = await mkdtemp(join(tmpdir(), "stow-startup-"));
    const binary = join(directory, "fake-stow");
    const pidFile = join(directory, "child.pid");
    const stopFile = join(directory, "child.stopped");
    const argsFile = join(directory, "child.args");
    const previous = {
      STOW_BIN: process.env.STOW_BIN,
      STOW_TEST_PID_FILE: process.env.STOW_TEST_PID_FILE,
      STOW_TEST_STOP_FILE: process.env.STOW_TEST_STOP_FILE,
      STOW_TEST_ARGS_FILE: process.env.STOW_TEST_ARGS_FILE,
    };
    const script = `#!/usr/bin/env node
const fs = require("node:fs");
fs.writeFileSync(process.env.STOW_TEST_PID_FILE, String(process.pid));
fs.writeFileSync(process.env.STOW_TEST_ARGS_FILE, JSON.stringify(process.argv));
process.stdout.write("STOW_READY endpoint=http://127.0.0.1:1 access_key=access secret_key=secret mode=local\\n");
const stop = () => {
  fs.writeFileSync(process.env.STOW_TEST_STOP_FILE, "terminated");
  process.exit(0);
};
process.on("SIGTERM", stop);
setInterval(() => {}, 1_000);
`;

    try {
      await writeFile(binary, script);
      await chmod(binary, 0o755);
      process.env.STOW_BIN = binary;
      process.env.STOW_TEST_PID_FILE = pidFile;
      process.env.STOW_TEST_STOP_FILE = stopFile;
      process.env.STOW_TEST_ARGS_FILE = argsFile;

      await assert.rejects(
        startStow({
          dataDir: join(directory, "data"),
          buckets: ["unavailable"],
          accessKey: "access",
          secretKey: "secret",
        }),
        /./,
      );

      const pid = Number(await readFile(pidFile, "utf8"));
      assert.equal(await readFile(stopFile, "utf8"), "terminated");
      const childArgs = JSON.parse(await readFile(argsFile, "utf8")) as string[];
      assert.equal(childArgs.includes("--access-key"), false);
      assert.equal(childArgs.includes("--secret-key"), false);
      assert.equal(childArgs.includes("secret"), false);
      await assertProcessExited(pid);
    } finally {
      restoreEnvironment(previous);
      await rm(directory, { recursive: true, force: true });
    }
  });
});

describe("fixture client lifecycle", () => {
  it("paginates through the shared iterator and destroys clients on errors", async () => {
    const server = await startFakeS3Server();
    const originalDestroy = S3Client.prototype.destroy;
    let destroyCalls = 0;
    S3Client.prototype.destroy = function destroy(): void {
      destroyCalls += 1;
      originalDestroy.call(this);
    };

    try {
      const instance = instanceFor(server.endpoint);
      await instance.createBucket("bucket");
      await instance.putFixture("bucket", "key", "body");
      const snapshots = await instance.snapshotObjects("bucket");
      assert.deepEqual(
        snapshots.map((snapshot) => snapshot.key),
        ["a", "b"],
      );
      await instance.emptyBucket("bucket");
      assert.equal(
        server.requests.filter((request) => request.startsWith("GET /bucket")).length,
        4,
      );
      assert.ok(
        server.requests.some((request) => request.includes("continuation-token=page-2")),
      );
      assert.equal(await verifyObjectReadable(instance, "bucket", "key"), "fixture body");

      const failed = instanceFor("http://127.0.0.1:1");
      await assert.rejects(failed.putFixture("bucket", "key", "body"));
      assert.equal(destroyCalls, 6);
    } finally {
      S3Client.prototype.destroy = originalDestroy;
      await server.close();
    }
  });
});

describe("optional SDK credentials", () => {
  it("preserves static credentials and passes through a session token or provider", () => {
    const tokenConfig = buildAwsSdkV3Config({
      endpoint: "http://127.0.0.1:9000",
      accessKeyId: "access",
      secretAccessKey: "secret",
      sessionToken: "token",
    });
    assert.deepEqual(tokenConfig.credentials, {
      accessKeyId: "access",
      secretAccessKey: "secret",
      sessionToken: "token",
    });
    const publicTokenConfig = Stow.awsSdkV3Config({
      endpoint: "http://127.0.0.1:9000",
      accessKeyId: "access",
      secretAccessKey: "secret",
      sessionToken: "token",
      forcePathStyle: false,
    });
    assert.deepEqual(publicTokenConfig.credentials, tokenConfig.credentials);
    assert.equal(publicTokenConfig.forcePathStyle, true);

    const provider = async () => ({
      accessKeyId: "provider-access",
      secretAccessKey: "provider-secret",
      sessionToken: "provider-token",
    });
    const providerConfig = buildAwsSdkV3Config({
      endpoint: "http://127.0.0.1:9000",
      accessKeyId: "unused",
      secretAccessKey: "unused",
      provider,
    });
    assert.equal(providerConfig.credentials, provider);
    const publicProviderConfig = Stow.awsSdkV3Config({
      endpoint: "http://127.0.0.1:9000",
      accessKeyId: "unused",
      secretAccessKey: "unused",
      provider,
    });
    assert.equal(publicProviderConfig.credentials, provider);

    const connection = Stow.connect({
      endpoint: "http://127.0.0.1:9000",
      accessKeyId: "unused",
      secretAccessKey: "unused",
      provider,
    });
    assert.equal(connection.awsSdkV3Config().credentials, provider);
    connection.disconnect();
  });
});
