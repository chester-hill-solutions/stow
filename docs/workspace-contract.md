# Stow Workspace Contract

**Status:** proposed
**Date:** 2026-09-25
**Scope:** the default agent-facing surface defined by
`docs/adr/0007-workspace-is-the-default.md`
**Relationship:** normative for the workspace path; additive to
`docs/compat-contract.md`, which continues to govern the S3 wire surface. Where
the two describe the same object, they must agree; where they differ, this
document describes the workspace and that one describes the wire.

Implementers and test authors MUST treat this as the acceptance spec. It exists
because ADR 0008 deliberately deferred the key-to-path encoding and the manifest
format, and because the encoding is where a workspace backend either works on
every platform or silently loses data on one.

---

## 0. The claim being specified

One call returns a bounded workspace that is already the caller's working
directory, and the same bytes are reachable through S3.

That claim has exactly two falsifiable halves, and both are conformance cases
in section 8:

1. A file written through the host's own filesystem tools, with stow never
   involved, is returned by `GetObject` with identical bytes.
2. An object written through `PutObject` exists on disk as a real file with
   identical bytes, at a path a person can open.

A workspace backend that satisfies only half of this is a store, and the
distinction is the whole product.

---

## 1. The workspace object

One workspace, one directory, one bucket, one identity. The directory is
mandatory: a workspace is a place, not a container of opaque bytes.

### 1.1 Go

```go
ws, err := stow.OpenWorkspace(stow.WorkspaceOptions{
    Dir:        dir,              // required; created if absent
    SessionID:  id,               // optional: resume instead of create
    MaxBytes:   64 << 20,
    MaxObjects: 10_000,
    TTL:        6 * time.Hour,
})
```

| Member | Guarantee |
|---|---|
| `ws.Dir() string` | The absolute workspace directory. Stable for the workspace's life. |
| `ws.Bucket() string` | The bucket over those bytes. Created before `OpenWorkspace` returns. |
| `ws.ID() string` | The durable session ID. Survives the process. |
| `ws.Path(key) (string, bool)` | Where a key lives, for a host that wants the path without an S3 round trip. `false` when the key is absent. |
| `ws.Facade() (*Facade, error)` | The opt-in loopback S3 endpoint, bound to this same instance. Idempotent. |
| `ws.Close() error` | Releases the client and any endpoint. **Does not delete data.** |
| `ws.Destroy(ctx) error` | Deletes the workspace. Explicit, never implied. |

### 1.2 TypeScript

```ts
const ws = await openWorkspace({ dir, sessionId, maxBytes, maxObjects, ttlMs });
```

`ws.dir`, `ws.bucket`, `ws.id`, `ws.path(key)`, `await ws.facade()`,
`await ws.close()`, `await ws.destroy()`, and a top-level
`resumeWorkspace(id)`.

### 1.3 Python

```python
from stow_s3 import open_workspace

ws = open_workspace(dir=..., session_id=..., max_bytes=..., ttl=...)
```

Same members. `close()` is non-destructive, which is the one place a Python
caller's existing muscle memory is wrong, and it is called out in the
changelog rather than discovered.

### 1.4 What every language agrees on

- `open` and `resume` are the same function; `sessionId` selects.
- `close` is idempotent and terminal for the *handle*. Later operations reject
  with `workspace_closed`.
- `destroy` is idempotent and succeeds on an already-destroyed workspace.
- Neither call is implicit. There is no path by which closing a handle deletes
  bytes, and no path by which a workspace reaches an upstream.

### 1.5 Delivery status

This document specifies the finished surface. It is ahead of the code, and
saying which member lands in which phase is more useful than a promise:

| Member | Status | Phase |
|---|---|---|
| `Dir`, `Bucket`, `ID`, `Path`, `Close` | **Shipped** in Go | W1 |
| The object surface, via the embedded runtime | **Shipped** in Go | W1 |
| `Destroy` | **Shipped** in Go, with the adoption guard it required | W3 |
| `open` / `resume` selected by one argument | **Shipped** in Go, via the registry | W4 |
| `Touch`, a registry on disk, and TTL collection | **Shipped** in Go, refusing live and adopted workspaces | W4 |
| `Facade` | Not yet. A workspace speaks no S3 today | W5 |
| TypeScript and Python surfaces | Not yet. The close/destroy split is settled and recorded in `docs/agent-dx-plan.md` §0.8; only the surface is missing | W7, and Python's is W7's known exception |

A member that does not exist yet is a compile error for a caller who reads this
document, which is the correct failure. None of them is a stub that returns
nothing and looks like success.

---

## 2. Opening a directory

`Dir` may name a directory that does not exist, one that stow created before,
or one a human already has files in. All three are supported, and the third is
the reason adoption matters.

| On open, the directory is… | Behavior |
|---|---|
| absent | Created, with a fresh workspace installed in it |
| present, holds a `.stow/` marker written by stow | Resumed in place. The existing manifest, bucket, and identity are adopted. |
| present, no marker | Adopted as a fresh workspace. Pre-existing files become readable as objects immediately. |
| present, `.stow/` present but the manifest is unreadable | **Refused.** `workspace_manifest_corrupt`. Stow does not guess and does not overwrite. |
| a path stow refuses to own | **Refused** under ADR 0006: no root, no home, no ancestor of either, and the filesystem root of any mounted volume. |

The third row is what makes `openWorkspace({ dir: process.cwd() })` a legal and
useful thing to write. Turning the directory an agent is already working in
into the workspace is the default case, not a special case.

Adoption is a first-class operation, not an import step. There is no separate
"scan and register" phase, because a workspace whose files need registering
before they can be read is not a working directory.

---

## 3. Key-to-path encoding

This is the section ADR 0008 section 2 deferred. It is the part that has to be
right on Windows, so it is specified rather than left to implementation.

### 3.1 Two forms, chosen per key

A key is stored at its **natural path** when the whole key can be that path
safely, and at an **escaped path** when it cannot. Both forms are visible on
disk; neither is hidden.

- **Natural:** `<dir>/<key>`, with `/` as the separator. `output/report.pdf`
  lives at `<dir>/output/report.pdf`.
- **Escaped:** `<dir>/.stow/keys/<digest>`, where `digest` is the lowercase
  hex SHA-256 of the key's UTF-8 bytes. The key itself is recorded in the
  manifest.

`.stow` is reserved. A key whose first segment is `.stow` is always escaped,
so the internal subtree is self-describing: a directory or file named `.stow`
at the root is stow's, and no user key can be spelled that way.

### 3.2 A key is natural only when all of these hold

1. Every segment is non-empty and is neither `.` nor `..`.
2. No segment contains NUL, `/` (beyond the separators), or a character the
   target filesystem cannot hold. On Windows that additionally excludes
   `< > : " \ | ? *`, a trailing dot or space, and the reserved device names
   `CON`, `PRN`, `AUX`, `NUL`, `COM1`–`COM9`, `LPT1`–`LPT9` in any case, with or
   without an extension.
3. The resulting path is within the platform's limit. A conservative
   240-byte cap is applied to the whole relative path, because Windows
   `MAX_PATH` is 260 by default and long-path support cannot be assumed.
4. No segment ends in the escape marker. The marker is `~`; a segment
   containing `~` is escaped. This keeps the two forms unambiguous by
   inspection, which is the rule the shard-prefix fix established for the
   record store: when a layout encodes structure in names, the names must be
   self-describing.
5. **No other key, and no adopted file, already occupies a
   case-insensitively equal path.** See 3.4.

A key failing any condition is escaped. It is never truncated, never rewritten
into something that collides, and never rejected: every key the S3 contract
accepts is storable, which is what makes the escaped form mandatory rather
than an optimization.

### 3.3 Resolution

`Path(key)` and `GetObject` resolve identically: compute the key's natural
candidate, accept it if 3.2 holds and something is there, otherwise use
`.stow/keys/<digest>`.

Listing enumerates the tree, skips `.stow`, and maps each relative path back to
its key. Escaped keys come from the manifest. A file present on disk with no
manifest entry is a key equal to its relative path — that is the adoption rule,
and it is why listing needs no separate import.

### 3.4 Case-insensitive filesystems

`Report.pdf` and `report.pdf` are two distinct S3 keys and one file on a
case-insensitive filesystem. Serving both from one path is data loss, and it is
silent.

The workspace backend therefore indexes every key it knows, plus every file it
finds in the directory, by case-folded relative path. A key is natural only if
its case-folded path is unique across that index. When two keys collide, the
first to be written keeps the natural path and every later one is escaped.

This is why the index is built on open rather than maintained lazily: an
adopted file whose name differs only in case from a key stow wrote must be
detected before either is served.

### 3.5 Deletion

A natural key unlinks its file. An escaped key removes `.stow/keys/<digest>`
and its manifest entry. Deleting an object never removes a directory that
still holds something, and never removes a file stow did not create without the
caller having asked for that key by name.

---

## 4. The manifest

One file: `<dir>/.stow/manifest.json`.

```json
{
  "version": 1,
  "workspace_id": "ws_9f2c...",
  "bucket": "stow-workspace-9f2c...",
  "created": "2026-09-25T18:04:11Z",
  "last_used": "2026-09-25T18:31:02Z",
  "ttl_seconds": 21600,
  "entries": {
    "output/report.pdf": {
      "form": "natural",
      "size": 182344,
      "etag": "9b2c...",
      "content_type": "application/pdf",
      "modified": "2026-09-25T18:30:58Z"
    },
    "weird/../key": {
      "form": "escaped",
      "digest": "3f0a...",
      "size": 12
    }
  }
}
```

Rules:

- **Versioned.** A `version` this build does not implement is refused, never
  migrated on read. The outbox already established that pattern for durable
  files, and a manifest that silently downgrades is a manifest that loses
  entries.
- **Atomically written.** Temporary file in `.stow/`, then rename. A manifest
  is never observed half-written.
- **Not the source of truth for existence.** The filesystem is. A manifest
  entry pointing at a missing file serves as absent; a file with no entry
  exists. The manifest carries metadata and identity, and losing it degrades
  metadata, never data.
- **A corrupt manifest refuses the open** rather than being rebuilt empty. An
  empty manifest would make every key resolve to absent while every file
  remained on disk, which reads as total data loss to the caller and is the
  worst available failure.

### 4.1 Staleness

A manifest records size and modification time. When either no longer matches
the file, the entry is stale: size, modification time, and content type are
re-derived from the file and the ETag is recomputed on read. The manifest is
updated opportunistically.

This is accepted rather than engineered away, per ADR 0008 section 5: an
agent may overwrite a file stow wrote, and refusing to serve a file stow
cannot prove it wrote would make the workspace useless for the work it exists
for.

---

## 5. Errors

Extends the code set in ADR 0004 section 5. S3 errors from the SDK pass through
unchanged.

| Code | Condition |
|---|---|
| `workspace_closed` | Operation on a handle after `close()` |
| `workspace_not_found` | Resume by an unknown or collected session ID |
| `workspace_manifest_corrupt` | The manifest exists and cannot be parsed or its version is unsupported |
| `workspace_not_owned` | The path is one stow refuses to own (ADR 0006) |
| `workspace_in_use` | Destroy or collect against a workspace a live session holds |
| `key_not_representable` | Reserved for a future encoding limit. The current encoding is total, so this must never fire; if it does, the encoding has a bug and the error is the assertion |

`workspace_in_use` is a correctness error, not policy. It is what stops a
collector from deleting a live agent's working directory.

---

## 6. Capabilities

Reported, never assumed, in the same spirit as ADR 0004:

```ts
ws.capabilities();
// { backend: "workspace", persistent: true, s3Facade: true, adoptForeignFiles:
//   true, caseInsensitiveHost: true, maxBytes, maxObjects, maxRequestBytes,
//   ttlSeconds, resumable: true, s3Session: false }
```

`caseInsensitiveHost` is reported because it changes observable behavior
(section 3.4) and a host that cares must be able to know. `s3Session` is false
on a workspace by construction: a workspace is not the S3 session profile, and a
capability set that blurred the two would let a caller assume a guarantee the
workspace does not make.

---

## 7. The facade

`Facade()` starts a loopback S3 endpoint bound to the **same** runtime instance:
same store, same quotas, same accounting. It is not a second store and there is
no synchronization between two, because there is only one.

- Idempotent: two calls return the same endpoint.
- The endpoint is loopback with generated credentials, and the credentials
  never appear in a log or on stdout.
- `close()` stops the endpoint. `destroy()` deletes the data.
- A facade's port is never exposed to anything but loopback, and a workspace
  never propagates a write upstream. A workspace that reached an upstream would
  be a live write with no opt-in, which ADR 0005 and ADR 0009 both forbid.

---

## 8. Conformance cases added to the corpus

These run against the workspace backend through the shared corpus, not as
hand-written expectations, per section 0.6 of `docs/agent-dx-plan.md`.

| ID | Case |
|---|---|
| WS-01 | A file created by the host's filesystem API, stow never involved, is returned by `GetObject` with identical bytes |
| WS-02 | An object created by `PutObject` exists on disk as a real file with identical bytes |
| WS-03 | A file stow did not write, with no manifest entry, is listed and readable |
| WS-04 | A 1024-byte key round-trips through the escaped form |
| WS-05 | A key containing `..`, a leading `/`, and a `~` round-trips and lands on the escaped form |
| WS-06 | Two keys differing only in case are two distinct objects, on a case-insensitive host |
| WS-07 | `DeleteObject` unlinks the file for a natural key and removes the manifest entry for an escaped one |
| WS-08 | Opening a directory with pre-existing files exposes them without an import step |
| WS-09 | A corrupt manifest refuses the open and deletes nothing |
| WS-10 | A manifest written by a newer version is refused, not downgraded |
| WS-11 | `close()` leaves every file in place; `destroy()` leaves nothing |
| WS-12 | A workspace over a full disk or an over-quota write fails without corrupting the manifest |
| WS-13 | Windows reserved device names, trailing dots, and trailing spaces all take the escaped form |
| WS-14 | The quota a host sets is the quota enforced, including a `MaxRequestBytes` the host raised |

WS-01 and WS-02 are the acceptance gate for the entire product direction. They
are the first two cases written and the first two run.

---

## 9. Not in this contract

- **Relay or tunnel for human download.** Out of scope per ADR 0009 section 6.
  A workspace is reachable from the machine that created it, which is the
  operator's own machine.
- **Promote to an upstream bucket.** The contract is ADR 0009 section 4; the
  implementation is blocked on a subsystem that has never propagated anything.
- **A shared daemon or a network registry.** The registry is a file in
  `.stow/`.
- **FUSE or an in-process mount.** A workspace is a real directory already. The
  S3 facade is the integration point; a kernel filesystem would be a second
  one.
- **Multi-writer coordination.** stow does not lock the directory. Two agents
  writing concurrently is allowed and last-writer-wins per key, which is the
  same guarantee the record store gives.
