# Stow v1 S3 Compatibility Contract

This document is the authoritative contract for `@chs/stow` v1. Implementers and test authors MUST treat it as the acceptance spec. Behavior not listed here as supported is unsupported unless explicitly noted as provider-tolerant (SDKs may send headers stow ignores).

**Scope:** Docker-free local S3-compatible dev bucket service (PGLite-inspired). Greenfield Go core + TypeScript npm wrapper.

**Non-goals (v1):** Versioning, Object Lock, ACLs, bucket policies, IAM, KMS/SSE, lifecycle rules, replication, notifications, S3 Select, batch operations.

---

## 1. Supported Operations

All S3 operations use **AWS Signature Version 4 (SigV4)** unless served via a **presigned URL** (query-string auth). Requests without valid auth receive `403 Forbidden` with an S3-shaped XML error body (see §5).

### 1.1 Bucket Operations

| Operation | HTTP | Path pattern | Required request headers | Success response |
|-----------|------|--------------|--------------------------|------------------|
| **ListBuckets** | `GET` | `/` | `Authorization` (SigV4) | `200 OK`, XML `ListAllMyBucketsResult`: `Owner`, zero or more `Buckets` (`Name`, `CreationDate` ISO8601) |
| **CreateBucket** | `PUT` | `/{bucket}` | `Authorization` | `200 OK` (empty body) for us-east-1-style; bucket created idempotently if it already exists |
| **HeadBucket** | `HEAD` | `/{bucket}` | `Authorization` | `200 OK` (empty body) if bucket exists; `404 Not Found` if missing |
| **DeleteBucket** | `DELETE` | `/{bucket}` | `Authorization` | `204 No Content` if empty bucket deleted; `404` if missing; `409 Conflict` if bucket contains objects |

**Notes:**
- Bucket names MUST follow S3 naming rules (3–63 chars, lowercase, DNS-compliant). Invalid names: `400 InvalidBucketName`.
- `CreateBucket` MUST NOT require `LocationConstraint` for v1 (single implicit region).
- `x-amz-acl`, `x-amz-grant-*`, and policy headers are ignored on supported bucket ops (no ACL enforcement).

### 1.2 Object Operations

| Operation | HTTP | Path pattern | Required request headers | Success response |
|-----------|------|--------------|--------------------------|------------------|
| **PutObject** | `PUT` | `/{bucket}/{key}` | `Authorization`, `Content-Length` | `200 OK`, XML body with `ETag` (quoted MD5 of object bytes for single-part PUT) |
| **GetObject** | `GET` | `/{bucket}/{key}` | `Authorization` | `200 OK`, object bytes; `Content-Type`, `Content-Length`, `ETag`, `Last-Modified`; user `x-amz-meta-*` echoed |
| **HeadObject** | `HEAD` | `/{bucket}/{key}` | `Authorization` | `200 OK` (empty body) with same metadata headers as GetObject; `404` if missing |
| **DeleteObject** | `DELETE` | `/{bucket}/{key}` | `Authorization` | `204 No Content` (even if key did not exist — S3 idempotent delete) |
| **DeleteObjects** | `POST` | `/{bucket}?delete` | `Authorization`, `Content-Type: application/xml`, `Content-Length` | `200 OK`, XML `DeleteResult` with per-key `Deleted` and/or `Error` entries |
| **CopyObject** | `PUT` | `/{bucket}/{key}` | `Authorization`, `x-amz-copy-source: /{srcBucket}/{srcKey}` (URL-encoded key segments) | `200 OK`, XML `CopyObjectResult` with `ETag`, `LastModified` |

**Object metadata (v1):**
- **Content-Type:** Stored and returned on GET/HEAD. Default `application/octet-stream` if omitted on PUT.
- **Content-Length:** Required on PUT; enforced. Mismatch between declared length and body: `400 Bad Request`.
- **ETag:** Strong validator; quoted hex MD5 for single-part objects; multipart ETag format per S3 (`{md5}-{partCount}`).
- **x-amz-meta-*:** Arbitrary user metadata keys (case-insensitive key normalization per S3). Returned on GET/HEAD. Max 2 KB total user metadata per object (enforce `400` if exceeded).
- **Range requests:** `GET` with `Range: bytes={start}-{end}` returns `206 Partial Content` with `Content-Range` header. Invalid or unsatisfiable range: `416 Range Not Satisfiable` with `Content-Range: bytes */{size}`.

**CopyObject constraints (v1):**
- Same-bucket and cross-bucket copy supported locally.
- `x-amz-metadata-directive` supported: `COPY` (default) or `REPLACE`.
- `x-amz-copy-source-if-match`, `if-none-match`, `if-modified-since`, `if-unmodified-since` supported; failed precondition → `412 Precondition Failed`.
- SSE/KMS copy headers ignored (no encryption at rest in v1).

### 1.3 Listing — ListObjectsV2

| Operation | HTTP | Path pattern | Query params | Required headers | Success response |
|-----------|------|--------------|--------------|------------------|------------------|
| **ListObjectsV2** | `GET` | `/{bucket}` | `list-type=2`, optional `prefix`, `delimiter`, `max-keys`, `continuation-token`, `start-after`, `encoding-type=url` | `Authorization` | `200 OK`, XML `ListBucketResult` |

**Required response fields:** `Name`, `Prefix`, `KeyCount`, `MaxKeys`, `IsTruncated`, `Contents[]` (when non-empty: `Key`, `LastModified`, `ETag`, `Size`, `StorageClass` = `STANDARD`), optional `CommonPrefixes[]` (`Prefix`), `ContinuationToken` (echo if sent), `NextContinuationToken` (if truncated), `Delimiter`, `EncodingType`.

**Pagination contract:**
- Default `max-keys` = 1000; server MAY use a lower internal page size but MUST honor `IsTruncated` + `NextContinuationToken` semantics.
- `continuation-token` is opaque; clients MUST round-trip the token verbatim.
- Listing is **lexicographic UTF-8 byte order** by key.

**Prefix / delimiter:**
- `prefix` filters keys by prefix.
- `delimiter` (typically `/`) groups keys; keys containing delimiter after prefix appear under `CommonPrefixes`, not `Contents`.

**URL encoding (`encoding-type=url`):**
- When `encoding-type=url`, keys and prefixes in XML MUST be URL-encoded; clients decode per AWS SDK behavior.

### 1.4 Multipart Upload Lifecycle

| Operation | HTTP | Path / query | Required headers | Success response |
|-----------|------|--------------|------------------|------------------|
| **CreateMultipartUpload** | `POST` | `/{bucket}/{key}?uploads` | `Authorization` | `200 OK`, XML `InitiateMultipartUploadResult` with `UploadId` |
| **UploadPart** | `PUT` | `/{bucket}/{key}?partNumber={n}&uploadId={id}` | `Authorization`, `Content-Length` | `200 OK`, XML with `ETag` (quoted MD5 of part) |
| **CompleteMultipartUpload** | `POST` | `/{bucket}/{key}?uploadId={id}` | `Authorization`, XML part list (`PartNumber`, `ETag`) | `200 OK`, XML `CompleteMultipartUploadResult` with final `ETag`, `Location` |
| **AbortMultipartUpload** | `DELETE` | `/{bucket}/{key}?uploadId={id}` | `Authorization` | `204 No Content` |
| **ListParts** | `GET` | `/{bucket}/{key}?uploadId={id}` | `Authorization` | `200 OK`, XML `ListPartsResult` |
| **ListMultipartUploads** | `GET` | `/{bucket}?uploads` | `Authorization` | `200 OK`, XML `ListMultipartUploadsResult` |

**Multipart rules:**
- Part numbers: integers `1`–`10000`.
- Minimum part size: 5 MiB for all parts except the last (S3 rule); smaller intermediate parts → `400 EntityTooSmall`.
- `UploadPart` MAY accept `Content-MD5` for validation; mismatch → `400 Bad Request`.
- Incomplete uploads do not appear in `ListObjectsV2` until completed.
- Aborted or expired uploads MUST NOT leave ghost objects.

### 1.5 Presigned URLs

Presigned URLs bypass the `Authorization` header; auth is carried in query parameters (`X-Amz-Algorithm`, `X-Amz-Credential`, `X-Amz-Date`, `X-Amz-Expires`, `X-Amz-SignedHeaders`, `X-Amz-Signature`).

| Capability | Methods | Notes |
|------------|---------|-------|
| **Presigned GET** | `GET` | Must validate signature and expiry; supports `Range` |
| **Presigned PUT** | `PUT` | Must validate signature and expiry; honors `Content-Type` and `Content-Length` signed headers when present |

Expired presigned URL → `403 AccessDenied` (`Request has expired`). Invalid signature → `403 AccessDenied`.

### 1.6 CORS

Stow MUST respond to browser preflight and cross-origin requests for presigned uploads/downloads.

| Request | Behavior |
|---------|----------|
| `OPTIONS` on object/bucket paths | `200 OK` with `Access-Control-Allow-Origin: *` (v1 dev default), `Access-Control-Allow-Methods: GET, PUT, POST, DELETE, HEAD`, `Access-Control-Allow-Headers: *`, `Access-Control-Expose-Headers: ETag, x-amz-meta-*` |
| Actual `PUT`/`GET` with `Origin` header | Include matching `Access-Control-Allow-Origin` on success and error responses |

Bucket-specific CORS XML configuration APIs are **out of scope**; CORS is a fixed permissive dev policy in v1.

---

## 2. SDK Flows to Test (Conformance Suite)

Each flow below MUST pass against the local endpoint using AWS SDK v3 (`@aws-sdk/client-s3`) with `endpoint`, `forcePathStyle` / virtual-hosted toggles, and stow-issued local dev credentials.

### 2.1 PutObject — Basic Write/Read Round-Trip

```
1. CreateBucket
2. PutObject(bucket, key, body, ContentType, Metadata: { "x-amz-meta-origin": "test" })
3. Assert response.ETag is quoted string
4. HeadObject → assert Content-Length, Content-Type, ETag, x-amz-meta-origin
5. GetObject → assert body bytes and metadata
6. DeleteObject
```

**Assertions:** ETag stable across HEAD/GET; metadata round-trip; `404` on missing key.

### 2.2 Multipart Upload — Large Object

```
1. CreateBucket
2. CreateMultipartUpload → capture uploadId
3. UploadPart × N (include one part < 5 MiB only as final part)
4. ListParts → assert part numbers and ETags
5. CompleteMultipartUpload
6. GetObject → assert full byte length and composite ETag format
7. DeleteObject
```

**Negative tests:** abort mid-upload → object not listable; complete with wrong ETag → `400 InvalidPart`; intermediate part < 5 MiB → `400 EntityTooSmall`.

### 2.3 Presigned URL — Browser-Compatible Upload and Download

```
1. PutObject seed object (or use presigned PUT)
2. Generate presigned GET URL (expires 300s) → fetch with undici/fetch → assert 200 + body
3. Generate presigned PUT URL → PUT new bytes with Content-Type → assert 200
4. HeadObject → object exists
5. Expired URL → assert 403
6. OPTIONS preflight on presigned PUT URL from synthetic Origin → assert ACAO + allowed methods
```

**Test both:** Node fetch and SDK `getSignedUrl` helpers.

### 2.4 ListObjectsV2 — Prefix, Delimiter, Pagination

```
1. PutObject keys: "a/1", "a/2", "b/1", "b/2"
2. ListObjectsV2(prefix="a/") → keys a/1, a/2
3. ListObjectsV2(delimiter="/") → CommonPrefixes a/, b/ (and root-level Contents if any)
4. Seed > max-keys objects → loop with continuation-token until IsTruncated=false
5. ListObjectsV2(encoding-type="url") → keys decoded by SDK match originals
```

### 2.5 Range GET — Partial Content

```
1. PutObject 10 KiB known pattern
2. GetObject Range bytes=0-1023 → 206, Content-Range, 1024 bytes
3. GetObject Range bytes=-512 → last 512 bytes
4. GetObject Range bytes=99999- → 416
```

### 2.6 CopyObject — Same-Bucket and Cross-Bucket

```
1. CreateBucket src, CreateBucket dst
2. PutObject(src, "original", body)
3. CopyObject(dst, "copy", CopySource=src/original) → assert ETag
4. GetObject(dst, "copy") → same bytes
5. CopyObject with MetadataDirective=REPLACE → new metadata, new values
6. CopyObject with CopySourceIfMatch=wrong → 412
```

### 2.7 DeleteObjects — Batch Delete

```
1. PutObject × 3 keys
2. DeleteObjects with 2 keys → response lists 2 Deleted
3. ListObjectsV2 → 1 key remains
4. DeleteObjects with quiet=false → per-key Deleted entries in XML
```

### 2.8 URL Style Matrix (smoke)

Run PutObject + GetObject with:
- Path-style: `endpoint/bucket/key` (`forcePathStyle: true`)
- Virtual-hosted: `bucket.endpoint/key` (`forcePathStyle: false`, bucket in Host header)

Both MUST succeed on the same bucket/object.

---

## 3. URL Style Support

| Style | Request form | v1 support |
|-------|--------------|------------|
| **Path-style** | `http://{host}:{port}/{bucket}/{key}` | **Required** |
| **Virtual-hosted-style** | `http://{bucket}.{host}:{port}/{key}` | **Required** |

**Routing rules:**
- Extract bucket from first path segment (path-style) or leftmost Host label before base host (virtual-hosted).
- Keys are the remaining path after bucket, URL-decoded per RFC 3986; preserve `/` in keys.
- `ListBuckets` uses `/` only (no bucket in Host).
- TLS termination is out of scope for local dev; plain HTTP on configurable port (default documented in package README).

**Invalid bucket in Host** (virtual-hosted): `400 Bad Request` with `InvalidBucketName` or `NoSuchBucket` as appropriate.

---

## 4. Authentication Requirements

### 4.1 SigV4 Header Auth (SDK default)

| Requirement | Contract |
|-------------|----------|
| Algorithm | `AWS4-HMAC-SHA256` only |
| Signed headers | Must include `host` and `x-amz-date` (and `x-amz-content-sha256` when present) |
| Credential scope | `{date}/{region}/s3/aws4_request` — region MUST match stow configured region (default `us-east-1`) |
| Access key | Stow-issued **local dev credentials** printed at startup / returned from `Stow.start()` |
| Secret key | Paired with local access key; NEVER upstream credentials for local endpoint auth |

**Failure responses:**
- Missing / malformed auth → `403 Forbidden`, code `AccessDenied` or `SignatureDoesNotMatch`
- Wrong secret → `403 SignatureDoesNotMatch`
- Request time skew > 15 minutes → `403 RequestTimeTooSkewed`

### 4.2 Presigned URLs

- Same local dev credentials used to sign.
- `X-Amz-Expires` maximum: **604800** seconds (7 days).
- Supported signed operations: `GET`, `PUT`, `HEAD` (HEAD via GET presign with SDK options).
- Unsigned query params not in signature MAY be ignored unless they alter signed headers/body.

### 4.3 Anonymous Access

Not supported. Unsigned requests (except presigned) → `403 AccessDenied`.

### 4.4 Upstream Credentials (Run-Through Only)

Upstream `STOW_*` / `S3_*` / `AWS_*` credentials are used **only** by the run-through adapter to reach live providers. They MUST NOT authenticate requests to the local stow endpoint.

---

## 5. Unsupported Features — Expected SDK Behavior

When a client invokes an unsupported API, stow MUST return S3-compatible XML errors so SDKs surface predictable exceptions.

| Feature / API | Example trigger | HTTP | Error `Code` | SDK expectation (AWS SDK v3) |
|---------------|-----------------|------|--------------|------------------------------|
| **Versioning** | `GetObject` with `versionId`, `PUT ?versioning` | `400` | `InvalidArgument` or `NotImplemented` | Operation fails; not silent no-op |
| **Object Lock** | `PUT` with `x-amz-object-lock-*` | `501` | `NotImplemented` | Clear failure |
| **ACLs** | `PUT` with `x-amz-acl: public-read` | `400` | `NotImplemented` | ACL ignored on supported ops; dedicated ACL APIs fail |
| **GetBucketAcl / PutBucketAcl** | ACL REST paths | `501` | `NotImplemented` | — |
| **Bucket policies** | `PutBucketPolicy` | `501` | `NotImplemented` | — |
| **IAM / STS** | `AssumeRole`, IAM-signing to non-stow principal | N/A | — | Out of stow scope; local creds only |
| **KMS / SSE** | `x-amz-server-side-encryption: aws:kms` | `400` | `InvalidArgument` | Encryption headers rejected on write |
| **Lifecycle** | `PutBucketLifecycleConfiguration` | `501` | `NotImplemented` | — |
| **Replication** | `PutBucketReplication` | `501` | `NotImplemented` | — |
| **Notifications** | `PutBucketNotificationConfiguration` | `501` | `NotImplemented` | — |
| **S3 Select** | `SELECT` object SQL | `501` | `NotImplemented` | — |
| **Batch operations** | `s3:CreateJob` | `501` | `NotImplemented` | — |
| **Tagging APIs** | `GetObjectTagging`, `PutObjectTagging` | `501` | `NotImplemented` | `x-amz-tagging` header on PutObject ignored |
| **Website / logging / accelerate** | Website configuration endpoints | `501` | `NotImplemented` | — |

**Error XML shape (all errors):**

```xml
<?xml version="1.0" encoding="UTF-8"?>
<Error>
  <Code>{Code}</Code>
  <Message>{human-readable message}</Message>
  <Resource>/{bucket}/{key}</Resource>
  <RequestId>{uuid}</RequestId>
</Error>
```

Every response MUST include `x-amz-request-id` header matching `RequestId` in body when body is XML.

---

## 6. Run-Through Mode Behavior Contract

Run-through mode is enabled by explicit config or **auto-detect** when upstream endpoint + access key + secret key are present in environment (`STOW_*` > `S3_*` > `AWS_*`). Override: `STOW_MODE=local` forces local-only.

### 6.1 Policies

| Policy | Reads | Writes |
|--------|-------|--------|
| **local-only** (default when no upstream) | Local store only | Local store only |
| **readThroughCache** (default when upstream detected) | Local miss → fetch upstream, cache locally, serve; hit → serve local with optional revalidation | **Local store only** unless `allowLiveWrites: true` |
| **readThroughCache + allowLiveWrites** | Same as above | Dual-write: local first, then upstream (upstream failure → surface error after local commit; document in logs) |

### 6.2 Read-Through Cache Semantics

```
GET/HeadObject flow (readThroughCache):
1. If object exists locally → return local bytes/metadata
   - If upstream configured AND revalidation enabled (default): compare ETag/Last-Modified with upstream HEAD
     - If upstream newer → refresh local copy, then serve
     - If upstream 404 → optionally evict local (config: evictOnUpstreamMissing, default true in run-through)
2. If local miss → HEAD/GET upstream
   - 404 → pass through 404 to client
   - 200 → persist to local backend, then serve
```

**ListObjectsV2 in run-through:**
- Returns **union** of local keys and upstream keys (deduplicated by key name).
- For duplicate keys, **local metadata wins** for `ETag`/`Size`/`LastModified` in listing (local is authoritative for dev).
- Pagination tokens MUST be stable for merged listing; conformance tests required.

**ListBuckets / HeadBucket / CreateBucket / DeleteBucket:**
- Local bucket namespace is authoritative.
- `CreateBucket` creates locally only; does not create upstream bucket unless `allowLiveWrites` and explicit bucket-mirror option (v1: **no auto-create upstream**).
- `DeleteBucket` deletes locally; upstream bucket untouched.

**CopyObject / Multipart / DeleteObject / DeleteObjects:**
- Execute against **local store only** in default run-through.
- With `allowLiveWrites: true`, mutating ops also propagate to upstream (same key path).

**Presigned URLs:**
- Signed against local endpoint; reads/writes hit local policy layer (not direct upstream bypass).

### 6.3 Startup Contract

On every `Stow.start()`, log to stdout (and expose via `/_stow/status`):

- Mode: `local-only` | `run-through`
- Upstream endpoint (host only; no secrets)
- Cache policy: `readThroughCache` | none
- Write policy: `local-only` | `allowLiveWrites`
- Override hints: `STOW_MODE=local`, `allowLiveWrites` flag

### 6.4 Conformance Tests (Run-Through)

Required manual/CI scenarios (against AWS S3, Cloudflare R2, or custom endpoint):

1. Local miss → upstream hit → local cache populated → second read served locally (mock upstream call count).
2. Local hit + upstream ETag change → revalidation refreshes object.
3. Upstream 404 on cached key → local evicted, client receives 404.
4. Write with default policy → upstream unchanged (verify with upstream SDK).
5. Write with `allowLiveWrites: true` → visible on upstream.

---

## 7. Admin Routes

Non-S3 HTTP routes for observability and debugging. **No SigV4 required** (local dev only; bind localhost by default).

| Route | Method | Auth | Response |
|-------|--------|------|----------|
| `/_stow/health` | `GET` | None | `200 OK` JSON: `{ "status": "ok" }` — liveness probe |
| `/_stow/status` | `GET` | None | `200 OK` JSON: mode, listen address, region, bucket count, object count (approx), cache policy, write policy, upstream endpoint (redacted), uptime seconds, version |
| `/_stow/inspect` | `GET` | None | `200 OK` JSON: detailed snapshot — buckets with object counts, in-flight multipart uploads, cache hit/miss counters, last upstream error (if any). Query `?bucket={name}` scopes to one bucket |

**Security:** Admin routes MUST NOT be exposed on `0.0.0.0` in default configuration; document risk if binding publicly.

**Errors:** Unknown `/_stow/*` paths → `404` JSON `{ "error": "not found" }`.

---

## Appendix A — Common HTTP Status Summary

| Status | When |
|--------|------|
| `200` | Successful GET/PUT/POST with XML body |
| `204` | Successful DELETE, DeleteObject |
| `206` | Range GET partial content |
| `400` | Invalid argument, bad multipart, metadata too large |
| `403` | Auth failure, expired presign |
| `404` | NoSuchBucket, NoSuchKey |
| `409` | BucketNotEmpty |
| `412` | Precondition failed (conditional copy/GET) |
| `416` | Invalid range |
| `501` | Unsupported S3 API |

---

## Appendix B — Versioning This Contract

- Breaking changes to any table in §1 or §6 require a major version bump of `@chs/stow`.
- New operations may be added in minor versions if marked **experimental** in changelog first.
- Conformance test suite in repo MUST reference this file by path and commit SHA in CI logs.
