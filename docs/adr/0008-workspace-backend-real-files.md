---
status: accepted
---

# A workspace stores objects as real files, with metadata in one manifest

This ADR decides the storage shape for the workspace default in ADR 0007. It
adds a backend; it does not change `internal/storage/fs`.

## Context

`internal/storage/fs` is a *store*, and it is a good one. Objects are atomic
JSON records written through a temporary file and a rename
(`internal/storage/fs/fs_records.go:26`), keys up to 1024 bytes are supported
by hex-encoding them across self-describing shard directories
(`internal/storage/fs/paths.go:18`), and the layout is an implementation
detail the S3 contract never sees.

That property is exactly what a workspace cannot have. A workspace's file
layout *is* its interface: a person opens it, an agent shells into it, a tool
writes into it without Stow's involvement. A sharded tree of base64 JSON
records satisfies none of that.

The tempting move is to reshape `internal/storage/fs` into a plain file
tree. That would trade away atomic record writes and the long-key sharding
that took two data-loss fixes to earn, in exchange for making every existing
guarantee conditional on a new layout. The two backends have genuinely
different contracts, so they should be two backends.

## Decisions

### 1. A third backend, not a change to the filesystem store

`internal/storage/fs` keeps its record format, its atomicity, and its
sharding. The workspace backend is a new implementation of the same
`storage.Store` contract, selected by name, and the two coexist. A data
directory written by one is never read by the other; the formats are
distinguishable on sight and a mismatched open is refused rather than
guessed at.

This follows the rule the shard-prefix fix established: when a layout
encodes structure in names, the names must be self-describing, and a
directory written by one version must not be silently reinterpreted by
another.

### 2. An object is a real file at a key-derived path

No sharding. A sharded tree is not a workspace, because `output/report.pdf`
has to be at `output/report.pdf`. The key-to-path mapping must be total and
reversible for every key the S3 contract accepts, including keys containing
`/`, keys whose characters a target filesystem cannot hold, and keys at the
1024-byte limit.

Where a key cannot be represented as a path, the operation fails with an S3
error naming the key. It never truncates, silently rewrites, or collides.
A reserved prefix encodes the characters that need it, chosen so the encoding
is unambiguous in one direction and does not depend on a stat.

**Specified in `docs/workspace-contract.md` section 3.** That section fixes the
natural/escaped split, the five conditions under which a key is natural, the
case-folding rule for case-insensitive filesystems, and the resolution and
deletion rules. It is normative; this ADR records only why the encoding is
needed and why it is total.

### 3. Metadata lives in one manifest per workspace, never per-object sidecars

One `stow-workspace.json` at the workspace root maps key to content type,
ETag, checksums, last-modified, and relative path. A missing entry is legal
and is the normal case for a file Stow did not write.

Per-object sidecar files are specifically rejected. That format already
existed and was abandoned: `.stowmeta` survives in the tree only so the
filesystem store can recognise and refuse it
(`internal/storage/fs/fs.go:20`). A single manifest also makes a workspace
inspectable with `cat`, which is the point of the product.

### 4. The backend adopts files it did not write

This is the requirement that makes it a workspace rather than a store. An
agent creates `output/report.pdf` with its own tools; Stow is never
involved. `GetObject("output/report.pdf")` must return those bytes.

So a file with no manifest entry is a valid object. Size and modification
time come from the file, the content type is sniffed, and the ETag is
computed from the content. Stow must not require that its own write path
produced every byte it serves.

### 5. The staleness that follows is accepted, not engineered away

Because Stow does not own the directory, an agent can overwrite a file
behind Stow's back. The manifest's recorded ETag is then wrong until
recomputed. The backend recomputes on read when the file's size or
modification time no longer matches the manifest, and does not lock the
workspace.

This is a deliberate trade. The alternative — refusing to serve a file
Stow cannot prove it wrote — would make the workspace unusable for the exact
workload it exists for, and a lock would deadlock the agent it is working
for.

### 6. Deletion keeps the ownership rule already established

Removing an object unlinks the file. Removing a workspace removes its
directory, and only when the workspace's own marker is present. The
refusals in ADR 0006 — no root, no home, no working directory, no ancestor
of any of them — apply unchanged, and a directory a caller supplied is never
removed without an explicit destroy.

## Consequences

- The workspace backend has a different durability story from the record
  store: an interrupted `PutObject` can leave a partial file. Writes go
  through a temporary file in the same directory and a rename, which bounds
  this to a partial file that the next read treats as absent rather than
  short. It does not provide the record format's all-or-nothing guarantee,
  and must not be advertised as providing one.
- Conformance gains a workspace-backend run. The existing corpus
  (`conformance/corpus/cases.json`) applies unchanged, which is the point of
  having a corpus.
- The same-bytes test in ADR 0007 section 5 is the workspace backend's
  acceptance gate, and it belongs in the corpus rather than in a unit test
  that could be made to pass by a mock.
- Windows path rules — reserved names, case-insensitivity, trailing dots and
  spaces — are part of the key-to-path mapping and must be covered by the
  same corpus. The workspace backend is a first-class target there, not a
  later port.
