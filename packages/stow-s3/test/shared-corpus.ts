import assert from "node:assert/strict";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
  AbortMultipartUploadCommand,
  CompleteMultipartUploadCommand,
  CopyObjectCommand,
  CreateMultipartUploadCommand,
  GetObjectCommand,
  HeadObjectCommand,
  ListMultipartUploadsCommand,
  ListObjectsV2Command,
  ListPartsCommand,
  PutObjectCommand,
  UploadPartCommand,
  type CompletedPart,
  type CopyObjectCommandOutput,
  type EncodingType,
  type GetObjectCommandOutput,
  type ListObjectsV2CommandOutput,
  type MetadataDirective,
  type PutObjectCommandInput,
  type PutObjectCommandOutput,
  type UploadPartCommandOutput,
  S3Client,
} from "@aws-sdk/client-s3";
import { Stow, type StowInstance } from "../dist/index.js";
import type {
  Corpus,
  CorpusCase,
  CorpusExpectation,
  CorpusPage,
  CorpusPart,
} from "./shared-corpus-types.js";

type CorpusBackend = "filesystem" | "memory";

export async function runSharedCorpus(backend: CorpusBackend): Promise<void> {
  const corpus = await loadCorpus();
  for (const testCase of corpus.cases) {
    await runCorpusCase(testCase, backend);
  }
}

async function loadCorpus(): Promise<Corpus> {
  const text = await readFile(
    new URL("../../../conformance/corpus/cases.json", import.meta.url),
    "utf8",
  );
  const corpus = JSON.parse(text) as Corpus;
  assert.equal(corpus.version, 1, "unsupported corpus version");
  assert.ok(corpus.cases.length > 0, "shared corpus must contain cases");
  const ids = new Set<string>();
  for (const testCase of corpus.cases) {
    assert.ok(testCase.id, "corpus case must have an id");
    assert.equal(ids.has(testCase.id), false, `duplicate corpus case ${testCase.id}`);
    ids.add(testCase.id);
    assert.ok(testCase.operation, `corpus case ${testCase.id} must have an operation`);
    assert.ok(testCase.expect.status > 0, `corpus case ${testCase.id} must have a status`);
  }
  return corpus;
}

async function runCorpusCase(testCase: CorpusCase, backend: CorpusBackend): Promise<void> {
  const dataDir = await mkdtemp(join(tmpdir(), `stow-corpus-${backend}-`));
  let instance: StowInstance | undefined;
  let client: S3Client | undefined;
  try {
    instance = await Stow.start({
      dataDir,
      buckets: corpusBuckets(testCase),
      port: 0,
      backend,
    });
    client = new S3Client(instance.awsSdkV3Config());
    await seedCorpus(client, testCase);
    await runCorpusOperation(client, testCase);
  } finally {
    client?.destroy();
    await instance?.stop();
    await rm(dataDir, { recursive: true, force: true });
  }
}

function corpusBuckets(testCase: CorpusCase): string[] {
  const buckets = new Set<string>([testCase.bucket]);
  for (const value of [testCase.sourceBucket, testCase.destinationBucket]) {
    if (value) buckets.add(value);
  }
  for (const object of testCase.setup ?? []) {
    if (object.bucket) buckets.add(object.bucket);
  }
  return [...buckets].sort();
}

async function seedCorpus(client: S3Client, testCase: CorpusCase): Promise<void> {
  for (const object of testCase.setup ?? []) {
    const output = await client.send(
      new PutObjectCommand({
        Bucket: object.bucket ?? testCase.bucket,
        Key: object.key,
        Body: object.body,
        ContentType: object.contentType,
        Metadata: object.metadata,
      }),
    );
    assert.equal(output.$metadata.httpStatusCode, 200);
  }
}

async function runCorpusOperation(client: S3Client, testCase: CorpusCase): Promise<void> {
  switch (testCase.operation) {
    case "putGetRoundTrip":
      await runPutGetRoundTrip(client, testCase);
      return;
    case "conditionalPut":
    case "checksumPut":
      await runPutWithOptions(client, testCase);
      return;
    case "conditionalGet":
      await runConditionalGet(client, testCase);
      return;
    case "listObjectsV2":
      await runListObjects(client, testCase);
      return;
    case "copyObject":
      await runCopyObject(client, testCase);
      return;
    case "multipartUpload":
      await runMultipartUpload(client, testCase);
      return;
    default:
      assert.fail(`unsupported corpus operation ${testCase.operation}`);
  }
}

async function runPutGetRoundTrip(client: S3Client, testCase: CorpusCase): Promise<void> {
  const put = await client.send(new PutObjectCommand(corpusPutInput(testCase)));
  const head = await client.send(
    new HeadObjectCommand({ Bucket: testCase.bucket, Key: testCase.key }),
  );
  assertStatus(head, 200);
  assert.equal(head.ETag, put.ETag, "Head ETag must match Put ETag");
  if (testCase.expect.contentType) assert.equal(head.ContentType, testCase.expect.contentType);
  assertMetadata(head.Metadata, testCase.expect.metadata, "HeadObject");
  await assertPutResult(client, testCase, put);
}

async function runConditionalGet(client: S3Client, testCase: CorpusCase): Promise<void> {
  const action = (): Promise<GetObjectCommandOutput> => client.send(new GetObjectCommand({
    Bucket: testCase.bucket,
    Key: testCase.key,
    IfMatch: testCase.ifMatch,
    IfNoneMatch: testCase.ifNoneMatch,
  }));
  // Anything other than 200 arrives as an error, and 304 is the case that is
  // easy to get wrong: Not Modified is a successful answer carrying no body, so
  // the SDK reports it rather than returning a GetObjectOutput. Verified
  // against @aws-sdk/client-s3, which is also what a caller of a real S3
  // endpoint sees.
  if (testCase.expect.status !== 200) {
    await assertCorpusError(action, testCase.expect);
    return;
  }
  await assertObject(await action(), testCase.expect);
}

async function runPutWithOptions(client: S3Client, testCase: CorpusCase): Promise<void> {
  const input = corpusPutInput(testCase);
  input.IfMatch = testCase.ifMatch;
  input.IfNoneMatch = testCase.ifNoneMatch;
  input.ContentMD5 = testCase.contentMD5;
  applyChecksum(input, testCase);
  const action = (): Promise<PutObjectCommandOutput> => client.send(new PutObjectCommand(input));
  if (testCase.expect.status >= 400) {
    await assertCorpusError(action, testCase.expect);
    return;
  }
  const put = await action();
  await assertPutResult(client, testCase, put);
}

async function assertPutResult(
  client: S3Client,
  testCase: CorpusCase,
  put: PutObjectCommandOutput,
): Promise<void> {
  assertStatus(put, testCase.expect.status);
  assertETag(put.ETag);
  assertChecksum(put, testCase.expect);
  const get = await client.send(
    new GetObjectCommand({ Bucket: testCase.bucket, Key: testCase.key }),
  );
  await assertObject(get, testCase.expect);
}

function corpusPutInput(testCase: CorpusCase): PutObjectCommandInput {
  return {
    Bucket: testCase.bucket,
    Key: testCase.key,
    Body: testCase.body ?? "",
    ContentType: testCase.contentType,
    Metadata: testCase.metadata,
  };
}

function applyChecksum(input: PutObjectCommandInput, testCase: CorpusCase): void {
  const algorithm = testCase.checksumAlgorithm;
  const value = testCase.checksumValue;
  if (!algorithm || !value) return;
  input.ChecksumAlgorithm = algorithm as PutObjectCommandInput["ChecksumAlgorithm"];
  switch (algorithm) {
    case "CRC32":
      input.ChecksumCRC32 = value;
      return;
    case "CRC32C":
      input.ChecksumCRC32C = value;
      return;
    case "SHA1":
      input.ChecksumSHA1 = value;
      return;
    case "SHA256":
      input.ChecksumSHA256 = value;
      return;
    default:
      assert.fail(`unsupported corpus checksum ${algorithm}`);
  }
}

async function runListObjects(client: S3Client, testCase: CorpusCase): Promise<void> {
  const pages = testCase.expect.pages ?? [{
    contents: testCase.expect.contents,
    commonPrefixes: testCase.expect.commonPrefixes,
    keyCount: testCase.expect.keyCount ?? 0,
    isTruncated: testCase.expect.isTruncated ?? false,
  }];
  let continuationToken: string | undefined;
  for (let index = 0; index < pages.length; index += 1) {
    const expected = pages[index];
    assert.ok(expected, `missing expected list page ${index + 1}`);
    const output = await client.send(new ListObjectsV2Command({
      Bucket: testCase.bucket,
      Prefix: testCase.prefix,
      Delimiter: testCase.delimiter,
      MaxKeys: testCase.maxKeys,
      ContinuationToken: continuationToken,
      EncodingType: testCase.encodingType as EncodingType | undefined,
    }));
    assertListPage(output, expected, index);
    if (index < pages.length - 1) {
      assert.equal(output.IsTruncated, true, `page ${index + 1} must be truncated`);
      assert.ok(output.NextContinuationToken, `page ${index + 1} needs a continuation token`);
      continuationToken = output.NextContinuationToken;
    } else {
      assert.equal(output.IsTruncated, false, `final page ${index + 1} must not be truncated`);
    }
  }
}

function assertListPage(output: ListObjectsV2CommandOutput, expected: CorpusPage, index: number): void {
  assert.equal(output.$metadata.httpStatusCode, 200);
  const contents = (output.Contents ?? []).flatMap((object) => object.Key ?? []);
  const prefixes = (output.CommonPrefixes ?? []).flatMap((entry) => entry.Prefix ?? []);
  assert.deepEqual(contents, expected.contents ?? [], `page ${index + 1} contents`);
  assert.deepEqual(prefixes, expected.commonPrefixes ?? [], `page ${index + 1} common prefixes`);
  assert.equal(output.KeyCount, expected.keyCount, `page ${index + 1} key count`);
}

async function runCopyObject(client: S3Client, testCase: CorpusCase): Promise<void> {
  assert.ok(testCase.sourceBucket && testCase.sourceKey && testCase.destinationKey);
  const destinationBucket = testCase.destinationBucket ?? testCase.bucket;
  const input = {
    Bucket: destinationBucket,
    Key: testCase.destinationKey,
    CopySource: `${testCase.sourceBucket}/${testCase.sourceKey}`,
    CopySourceIfMatch: testCase.copySourceIfMatch,
    CopySourceIfNoneMatch: testCase.copySourceIfNoneMatch,
  } as const;
  const command = new CopyObjectCommand(input);
  if (testCase.metadataDirective) {
    command.input.MetadataDirective = testCase.metadataDirective as MetadataDirective;
    command.input.ContentType = testCase.contentType;
    command.input.Metadata = testCase.metadata;
  }
  const action = (): Promise<CopyObjectCommandOutput> => client.send(command);
  if (testCase.expect.status >= 400) {
    await assertCorpusError(action, testCase.expect);
    return;
  }
  const output = await action();
  assertStatus(output, testCase.expect.status);
  assertETag(output.CopyObjectResult?.ETag);
  const get = await client.send(new GetObjectCommand({
    Bucket: destinationBucket,
    Key: testCase.destinationKey,
  }));
  await assertObject(get, testCase.expect);
}

async function runMultipartUpload(client: S3Client, testCase: CorpusCase): Promise<void> {
  assert.ok(testCase.key && testCase.parts && testCase.parts.length > 0);
  const created = await client.send(new CreateMultipartUploadCommand({
    Bucket: testCase.bucket,
    Key: testCase.key,
  }));
  assert.equal(created.$metadata.httpStatusCode, 200);
  assert.ok(created.UploadId, "CreateMultipartUpload must return an upload ID");
  const uploadId = created.UploadId;
  let completed = false;
  try {
    if (testCase.listMultipartUploads) await assertMultipartListing(client, testCase, uploadId);
    const completedParts: CompletedPart[] = [];
    const partBodies: Buffer[] = [];
    for (const part of testCase.parts) {
      const body = corpusPartBody(part);
      const uploaded: UploadPartCommandOutput = await client.send(new UploadPartCommand({
        Bucket: testCase.bucket,
        Key: testCase.key,
        UploadId: uploadId,
        PartNumber: part.number,
        Body: body,
      }));
      assert.equal(uploaded.$metadata.httpStatusCode, 200);
      assertETag(uploaded.ETag);
      completedParts.push({ ETag: uploaded.ETag, PartNumber: part.number });
      partBodies.push(body);
    }
    await assertPartsListing(client, testCase, uploadId);
    const finished = await client.send(new CompleteMultipartUploadCommand({
      Bucket: testCase.bucket,
      Key: testCase.key,
      UploadId: uploadId,
      MultipartUpload: { Parts: completedParts },
    }));
    completed = true;
    assertStatus(finished, testCase.expect.status);
    if (testCase.expect.etag) assert.equal(finished.ETag, testCase.expect.etag);
    else assertETag(finished.ETag);
    const expectedBody = Buffer.concat(partBodies);
    if (testCase.expect.bodyLength) {
      assert.equal(expectedBody.length, testCase.expect.bodyLength);
    }
    const get = await client.send(new GetObjectCommand({ Bucket: testCase.bucket, Key: testCase.key }));
    assert.equal(get.$metadata.httpStatusCode, 200);
    const body = Buffer.from(await get.Body?.transformToByteArray() ?? []);
    assert.equal(body.equals(expectedBody), true, "multipart body must equal concatenated parts");
  } finally {
    if (!completed) {
      await client.send(new AbortMultipartUploadCommand({
        Bucket: testCase.bucket,
        Key: testCase.key,
        UploadId: uploadId,
      })).catch(() => undefined);
    }
  }
}

async function assertMultipartListing(client: S3Client, testCase: CorpusCase, uploadId: string): Promise<void> {
  const output = await client.send(new ListMultipartUploadsCommand({ Bucket: testCase.bucket }));
  assert.equal(output.$metadata.httpStatusCode, 200);
  assert.equal(output.Uploads?.length, 1);
  assert.equal(output.Uploads?.[0]?.Key, testCase.key);
  assert.equal(output.Uploads?.[0]?.UploadId, uploadId);
}

async function assertPartsListing(client: S3Client, testCase: CorpusCase, uploadId: string): Promise<void> {
  const output = await client.send(new ListPartsCommand({
    Bucket: testCase.bucket,
    Key: testCase.key,
    UploadId: uploadId,
  }));
  assert.equal(output.$metadata.httpStatusCode, 200);
  const actual = (output.Parts ?? []).map((part) => part.PartNumber);
  const expected = testCase.expect.partNumbers ?? testCase.parts?.map((part) => part.number) ?? [];
  assert.deepEqual(actual, expected);
}

function corpusPartBody(part: CorpusPart): Buffer {
  const repeat = part.repeat && part.repeat > 0 ? part.repeat : 1;
  return Buffer.from((part.body ?? "").repeat(repeat));
}

async function assertObject(output: GetObjectCommandOutput, expected: CorpusExpectation): Promise<void> {
  assert.equal(output.$metadata.httpStatusCode, 200);
  const body = await output.Body?.transformToString() ?? "";
  assert.equal(body, expected.body ?? "");
  if (expected.contentType) assert.equal(output.ContentType, expected.contentType);
  assertMetadata(output.Metadata, expected.metadata, "GetObject");
}

function assertMetadata(actual: Record<string, string> | undefined, expected: Record<string, string> | undefined, label: string): void {
  for (const [key, value] of Object.entries(expected ?? {})) {
    assert.equal(actual?.[key], value, `${label} metadata ${key}`);
  }
}

function assertStatus(output: { $metadata: { httpStatusCode?: number } }, expected: number): void {
  assert.equal(output.$metadata.httpStatusCode, expected);
}

function assertETag(etag: string | undefined): void {
  assert.ok(etag && etag.replaceAll('"', "").length > 0, "expected a non-empty ETag");
}

function assertChecksum(output: PutObjectCommandOutput, expected: CorpusExpectation): void {
  if (!expected.checksumAlgorithm) return;
  const values: Record<string, string | undefined> = {
    CRC32: output.ChecksumCRC32,
    CRC32C: output.ChecksumCRC32C,
    SHA1: output.ChecksumSHA1,
    SHA256: output.ChecksumSHA256,
  };
  assert.equal(values[expected.checksumAlgorithm], expected.checksumValue);
}

async function assertCorpusError(
  action: () => Promise<unknown>,
  expected: CorpusExpectation,
): Promise<void> {
  let failure: unknown;
  try {
    await action();
  } catch (error) {
    failure = error;
  }
  assert.ok(failure !== undefined, "expected corpus operation to fail");
  assert.equal(errorStatus(failure), expected.status);
  if (expected.errorCode) assert.equal(errorCode(failure), expected.errorCode);
}

function errorStatus(error: unknown): number | undefined {
  if (!isRecord(error)) return undefined;
  const metadata = error["$metadata"];
  if (!isRecord(metadata)) return undefined;
  const status = metadata["httpStatusCode"];
  return typeof status === "number" ? status : undefined;
}

function errorCode(error: unknown): string | undefined {
  if (!isRecord(error)) return undefined;
  for (const key of ["Code", "code", "name"]) {
    const value = error[key];
    if (typeof value === "string") return value;
  }
  const message = error["message"];
  if (typeof message !== "string") return undefined;
  return /<Code>([^<]+)<\/Code>/.exec(message)?.[1];
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}
