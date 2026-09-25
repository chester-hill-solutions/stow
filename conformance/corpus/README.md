# Shared conformance corpus

`cases.json` is a language-neutral, declarative contract for SDK-observable S3
behavior. The Go and Node runners are adapters: they create the declared
fixtures, issue the declared operation, and compare the response with
`expect`. Protocol edges that need raw HTTP (malformed signatures, unsupported
semantic markers, and browser preflight) stay in the focused protocol tests.

## Case shape

Every case has an `id`, an `operation`, a logical `bucket`, and an `expect`
object with a required `status`. The common optional fields are `key`, `body`,
`contentType`, and `metadata`. `setup` contains objects created before the
operation; an omitted setup bucket means the case bucket.

| Operation | Request fields | Assertions |
|-----------|----------------|------------|
| `putGetRoundTrip` | common object fields | status, ETag, body, content type, metadata |
| `conditionalPut` / `conditionalGet` | `ifMatch` and/or `ifNoneMatch` | status and S3 error code, or the stored result |
| `checksumPut` | `contentMD5` or `checksumAlgorithm`/`checksumValue` | status/error code, body, and returned checksum |
| `listObjectsV2` | `prefix`, `delimiter`, `maxKeys`, `encodingType` | ordered `expect.pages` with contents, common prefixes, key count, and truncation |
| `copyObject` | source/destination fields, optional directive and source preconditions | status/error code, ETag, destination object |
| `multipartUpload` | `parts` (`number`, `body`, optional `repeat`) and optional upload listing | create/upload/list/complete, concatenated body, composite ETag |

`repeat` expands a part body without putting megabytes of literal data in the
JSON file. A value of `1` (or omitted) means one copy; the current multipart
case uses `5242880` to satisfy the S3 five-mebibyte minimum for a non-final
part.

## Adding a case

1. Add a uniquely named case with declarative setup and expectations.
2. Add or reuse an operation adapter in both
   `conformance/corpus_*.go` and `packages/stow-s3/test/shared-corpus.ts`.
3. Run `make test-conformance` and the Node package test. A case that only
   works in one runner is not a shared contract case.

Bucket names in the JSON are logical names. The Go runner maps them to unique
per-case buckets so the filesystem and memory matrices remain isolated; the
Node runner creates the same logical set in a fresh temporary data directory.
