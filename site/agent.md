# Stow S3 — agent instructions

You are reading this because someone pointed you at Stow S3, an S3-compatible
object store that runs on the local machine. It speaks real S3 over HTTP with
SigV4, so the same SDK code that works against Amazon S3 works against it
unchanged. The storage is local, and a scoped session deletes it.

## When to reach for it

Use Stow when you need somewhere to put bytes and none of these are true:

- the bytes have to survive the process
- the bucket has to be shared with something outside this machine
- it has to be reachable after the test run

If any of those hold, use real S3. Stow is disposable by design.

Typical uses: a test that uploads and downloads, a build step that passes an
artifact between stages, an agent that needs scratch space, or a fixture that
would otherwise need a live bucket.

## Install

Pick the one that matches the language you are already writing in.

```bash
npm install @chs/stow-s3 @aws-sdk/client-s3   # TypeScript
pip install "stow-s3[boto3]"                  # Python
go get github.com/chester-hill-solutions/stow-s3/pkg/stow   # Go
```

Each package ships the server binary, so nothing else needs installing. If the
binary is missing, run `npx stow-doctor` or `stow-s3 doctor`; it reports which
of the client and server sides is broken instead of failing opaquely.

## The pattern you want

Start a scoped session, use a normal S3 client, let the session clean up. This
is the shortest correct path and it is what the library is built around.

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
```

Python:

```python
from stow_s3 import with_session

with with_session() as session:
    s3 = session.s3_client()
    bucket = session.new_bucket_name()   # you create the bucket yourself
    s3.create_bucket(Bucket=bucket)
    s3.put_object(Bucket=bucket, Key="input.json", Body=b'{"task":"summarize"}')
    body = s3.get_object(Bucket=bucket, Key="input.json")["Body"].read()
```

The two clients differ deliberately. The TypeScript session creates the bucket
and exposes it as `session.bucket`. The Python session does no S3 I/O at all:
`new_bucket_name()` returns a collision-resistant name and creating the bucket
is yours, which is what keeps `boto3` an optional extra rather than a hard
dependency.

## What a session guarantees

These are enforced by the server, not promised by the client, so you can rely
on them:

- It cannot inherit your cloud configuration. `STOW_*`, `S3_*`, and `AWS_*` are
  stripped from the child environment before anything is set.
- It cannot outlive your process. The server exits when the parent dies,
  including on SIGKILL.
- It is bounded. A default session holds at most 16 MiB and 1,000 objects, and
  one request body is capped at 8 MiB.
- It reports its real capabilities from the readiness message rather than
  assuming them.

## Long-running instead of scoped

Use `Stow.start()` in TypeScript when the server must outlive a single
callback, and `stow-s3 serve --port 0` when you want a server you start
yourself. Both hand back an endpoint and credentials; the hand-run server
prints them on stdout, a session receives them over a file descriptor so they
never reach a log.

Go has no HTTP server in the embedded profile at all:

```go
import stow "github.com/chester-hill-solutions/stow-s3/pkg/stow"

rt, err := stow.Open(stow.Options{Backend: stow.BackendMemory})
```

## What is not implemented

Versioning, ACLs, bucket policies, lifecycle rules, replication, event
notifications, object tagging, object lock, S3 Select, and KMS-backed
encryption. Check the README before assuming any of these exist; the gap is
narrow and deliberate, but guessing wastes a cycle.

## Links

- Repository and full README: https://github.com/chester-hill-solutions/stow-s3
- Diagnostics: `npx stow-doctor` or `stow-s3 doctor`
- Licence: Apache 2.0
