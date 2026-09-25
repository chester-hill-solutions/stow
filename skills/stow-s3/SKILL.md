---
name: stow-s3
description: Local S3-compatible object store for tests, CI, dev, and agents. Use when a test or script needs to upload, download, or pass S3 objects but no real bucket should be provisioned; when an agent needs scratch storage; when a fixture needs a bucket that is thrown away afterwards; or when asked to mock, fake, stub, or localise S3. Provides the withStow scoped-session pattern in TypeScript and Python, the Go embedded runtime, and the stow-s3 CLI. Triggers on "needs an S3 bucket in a test", "don't want to hit real S3 in CI", "S3 mock", "localstack alternative", "give the agent a bucket", "put an artifact somewhere in a build".
---

# Stow S3

An S3-compatible object store that runs on the local machine. Real S3 over
HTTP with SigV4, so existing SDK code works unchanged. Nothing to provision, no
account, and a scoped session deletes itself when it closes.

Repository: https://github.com/chester-hill-solutions/stow-s3
Machine-readable instructions: https://stow.chesterhillsolutions.ca/agent.md

## Decide first

Reach for Stow only when **all** of these hold:

- the bytes do not have to survive the process
- the bucket does not have to be shared with another machine
- the data does not have to be there after the test run

If any one of them does, use real S3. Stow is disposable by design, and
pretending otherwise wastes the next debugging session.

If the code only calls two or three S3 operations and never asserts on
behaviour you care about, a plain in-memory fake is less machinery. Stow is
worth it when the code exercises the SDK itself — signing, multipart, range
reads, error shapes.

## Install

```bash
npm install @chs/stow-s3 @aws-sdk/client-s3   # TypeScript
pip install "stow-s3[boto3]"                  # Python
go get github.com/chester-hill-solutions/stow-s3/pkg/stow   # Go
```

Each package ships the server binary, so there is nothing else to install and
no PATH entry to set. If the binary is missing, `npx stow-doctor` (TypeScript)
or `stow-s3 doctor` (server side) reports which of the two sides is broken
rather than failing opaquely.

## The scoped session

This is the pattern to reach for by default: one call, a private server on an
ephemeral port, a bucket already created, and cleanup on close.

TypeScript:

```ts
import { GetObjectCommand, PutObjectCommand } from "@aws-sdk/client-s3";
import { withStow } from "@chs/stow-s3";

const body = await withStow(async ({ s3, bucket }) => {
  await s3.send(new PutObjectCommand({
    Bucket: bucket,
    Key: "input.json",
    Body: '{"task":"summarize"}',
  }));

  const got = await s3.send(new GetObjectCommand({
    Bucket: bucket,
    Key: "input.json",
  }));
  return got.Body?.transformToString();
});
// server and data are gone here, even if the callback threw
```

Python:

```python
from stow_s3 import with_session

with with_session() as session:
    s3 = session.s3_client()
    bucket = session.new_bucket_name()   # collision-resistant; you create it
    s3.create_bucket(Bucket=bucket)
    s3.put_object(Bucket=bucket, Key="input.json", Body=b'{"task":"summarize"}')
    body = s3.get_object(Bucket=bucket, Key="input.json")["Body"].read()
```

The two clients differ deliberately and the difference is not a bug: the
TypeScript session creates the bucket and exposes it as `session.bucket`, while
the Python session performs no S3 I/O and leaves `create_bucket` to you. That
is what keeps `boto3` an optional extra rather than a hard dependency.

## What the session actually guarantees

Server-enforced, so these are safe to rely on:

- `STOW_*`, `S3_*`, and `AWS_*` are stripped from the child environment before
  anything is set, so a stray cloud credential cannot turn a local session into
  a run-through one.
- The server exits when its parent process dies, including on SIGKILL.
- Defaults: 16 MiB and 1,000 objects per session, 8 MiB per request body. The
  server enforces them on every request; the client does not police them.
- Capabilities and limits come from the readiness message, so a client never
  reports a limit the server has not confirmed.

## Other shapes

- `Stow.start()` (TypeScript) — a server that outlives one callback, for a
  suite that shares one endpoint.
- `stow-s3 serve --port 0` — a server you start yourself. A hand-run server
  prints its endpoint and credentials on stdout; a session receives them over
  an inherited file descriptor so they never reach a log.
- Go, in-process with no HTTP listener and no credentials:

```go
import stow "github.com/chester-hill-solutions/stow-s3/pkg/stow"

rt, err := stow.Open(stow.Options{Backend: stow.BackendMemory})
```

Backends are `memory` (throwaway), `filesystem` (survives a restart), and
run-through (local in front of an upstream bucket).

## Not implemented

Versioning, ACLs, bucket policies, lifecycle rules, replication, event
notifications, object tagging, object lock, S3 Select, KMS-backed encryption.

Implemented: bucket CRUD, object put/get/head/copy/delete, prefix listing with
pagination, range reads, conditional reads and writes, MD5/CRC32/CRC32C/SHA-1/
SHA-256 checksums, multipart uploads, presigned requests, SigV4.

## Reporting problems

Include the output of `npx stow-doctor --json`, the language and version, and
the S3 operation that failed. That is enough to reproduce almost anything.
