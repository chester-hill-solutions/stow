import assert from "node:assert/strict";
import { chmod, copyFile, mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { pathToFileURL } from "node:url";
import { describe, it } from "node:test";
import { stowBinaryAvailable } from "../dist/bin.js";
import { verifyObjectReadable } from "../dist/instance.js";
import { GetObjectCommand, HeadObjectCommand, PutObjectCommand, S3Client } from "@aws-sdk/client-s3";
import { Stow } from "../dist/index.js";
import { parseReadyLine } from "../dist/start.js";

describe("parseReadyLine", () => {
  it("parses the STOW_READY banner line", () => {
    const ready = parseReadyLine(
      "STOW_READY endpoint=http://127.0.0.1:54321 access_key=ABCDEF secret_key=ghijklmnop mode=local",
    );
    assert.equal(ready?.endpoint, "http://127.0.0.1:54321");
    assert.equal(ready?.accessKeyId, "ABCDEF");
    assert.equal(ready?.secretAccessKey, "ghijklmnop");
    assert.equal(ready?.mode, "local");
  });
});

describe("stow binary discovery", () => {
  it("finds an executable on PATH", async () => {
    if (process.platform === "win32") {
      return;
    }
    const dir = await mkdtemp(join(tmpdir(), "stow-path-"));
    const binary = join(dir, "stow");
    await writeFile(binary, "#!/bin/sh\nexit 0\n");
    await chmod(binary, 0o755);
    const isolatedDist = join(dir, "isolated", "dist");
    await mkdir(isolatedDist, { recursive: true });
    await copyFile(new URL("../dist/bin.js", import.meta.url), join(isolatedDist, "bin.js"));
    const isolated = await import(pathToFileURL(join(isolatedDist, "bin.js")).href);
    const previousPath = process.env.PATH;
    process.env.PATH = dir;
    try {
      assert.equal(isolated.resolveStowBinary(), "stow");
      assert.equal(isolated.stowBinaryAvailable(), true);
    } finally {
      if (previousPath === undefined) {
        delete process.env.PATH;
      } else {
        process.env.PATH = previousPath;
      }
      await rm(dir, { recursive: true, force: true });
    }
  });
});

interface SharedCase {
  id: string;
  operation: string;
  bucket: string;
  key: string;
  body: string;
  contentType?: string;
  metadata?: Record<string, string>;
  expect: {
    status: number;
    body: string;
    contentType?: string;
    metadata?: Record<string, string>;
  };
}

describe("shared conformance corpus", () => {
  it("runs the SDK round-trip case", async () => {
    const corpus = JSON.parse(
      await readFile(new URL("../../../conformance/corpus/cases.json", import.meta.url), "utf8"),
    ) as { cases: SharedCase[] };
    const testCase = corpus.cases.find((candidate) => candidate.id === "put-get-roundtrip");
    assert.ok(testCase, "shared corpus must contain put-get-roundtrip");

    const dataDir = await mkdtemp(join(tmpdir(), "stow-corpus-"));
    const instance = await Stow.start({
      dataDir,
      buckets: [testCase.bucket],
      port: 0,
      backend: "filesystem",
    });
    const client = new S3Client(instance.awsSdkV3Config());
    try {
      const put = await client.send(
        new PutObjectCommand({
          Bucket: testCase.bucket,
          Key: testCase.key,
          Body: testCase.body,
          ContentType: testCase.contentType,
          Metadata: testCase.metadata,
        }),
      );
      assert.equal(put.$metadata.httpStatusCode, testCase.expect.status);
      const head = await client.send(
        new HeadObjectCommand({ Bucket: testCase.bucket, Key: testCase.key }),
      );
      assert.equal(head.ContentType, testCase.expect.contentType);
      for (const [key, value] of Object.entries(testCase.expect.metadata ?? {})) {
        assert.equal(head.Metadata?.[key], value);
      }
      const get = await client.send(
        new GetObjectCommand({ Bucket: testCase.bucket, Key: testCase.key }),
      );
      assert.equal(await get.Body?.transformToString(), testCase.expect.body);
    } finally {
      client.destroy();
      await instance.stop();
      await rm(dataDir, { recursive: true, force: true });
    }
  });
});

describe("external connections", () => {
  it("connects without taking ownership of a process", async () => {
    const dataDir = await mkdtemp(join(tmpdir(), "stow-connect-"));
    const instance = await Stow.start({ dataDir, port: 0 });
    try {
      const connection = Stow.connect({
        endpoint: instance.endpoint,
        accessKeyId: instance.accessKeyId,
        secretAccessKey: instance.secretAccessKey,
        region: instance.region,
      });
      assert.equal(connection.endpoint, instance.endpoint);
      assert.equal(connection.awsSdkV3Config().forcePathStyle, true);
      connection.disconnect();
    } finally {
      await instance.stop();
      await rm(dataDir, { recursive: true, force: true });
    }
  });
});

describe("shared conformance corpus", () => {
  it("runs the SDK round-trip case with the memory backend", async () => {
    const corpus = JSON.parse(
      await readFile(new URL("../../../conformance/corpus/cases.json", import.meta.url), "utf8"),
    ) as { cases: SharedCase[] };
    const testCase = corpus.cases.find((candidate) => candidate.id === "put-get-roundtrip");
    assert.ok(testCase, "shared corpus must contain put-get-roundtrip");

    const dataDir = await mkdtemp(join(tmpdir(), "stow-corpus-memory-"));
    const instance = await Stow.start({
      dataDir,
      buckets: [testCase.bucket],
      port: 0,
      backend: "memory",
    });
    const client = new S3Client(instance.awsSdkV3Config());
    try {
      await client.send(
        new PutObjectCommand({
          Bucket: testCase.bucket,
          Key: testCase.key,
          Body: testCase.body,
          ContentType: testCase.contentType,
          Metadata: testCase.metadata,
        }),
      );
      const get = await client.send(
        new GetObjectCommand({ Bucket: testCase.bucket, Key: testCase.key }),
      );
      assert.equal(await get.Body?.transformToString(), testCase.expect.body);
    } finally {
      client.destroy();
      await instance.stop();
      await rm(dataDir, { recursive: true, force: true });
    }
  });
});

const integration = describe;
integration("integration", { skip: !stowBinaryAvailable() }, () => {
  it("starts, writes, reads, and stops", async () => {
    const dataDir = await mkdtemp(join(tmpdir(), "stow-test-"));
    const instance = await Stow.start({
      dataDir,
      buckets: ["uploads"],
      port: 0,
      cleanSlate: true,
    });

    try {
      assert.match(instance.endpoint, /^http:\/\/127\.0\.0\.1:\d+$/);
      assert.ok(instance.accessKeyId.length > 0);
      assert.ok(instance.secretAccessKey.length > 0);

      const config = instance.awsSdkV3Config();
      assert.equal(config.endpoint, instance.endpoint);
      assert.equal(config.forcePathStyle, true);

      await instance.putFixture("uploads", "hello.txt", "hello world");
      const body = await verifyObjectReadable(instance, "uploads", "hello.txt");
      assert.equal(body, "hello world");

      const snapshot = await instance.snapshotObjects("uploads");
      assert.deepEqual(snapshot.map((item) => item.key), ["hello.txt"]);
    } finally {
      await instance.stop();
    }
  });
});
