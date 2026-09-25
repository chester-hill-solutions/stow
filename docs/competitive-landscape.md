# Competitive landscape: local S3 emulation, self-hosted S3 servers, and the MinIO-EOL question

**Status:** research snapshot. All external sources accessed **2026-09-25**.

## Method and evidence rules

- Competitor facts come only from first-party sources: official documentation sites, official
  repositories, and vendor project pages. No blog posts, listicles, aggregators, or third-party
  benchmarks.
- **Stow's capabilities are derived from the repository's source code only.** `README.md`,
  `CONTEXT.md`, `CHANGELOG.md`, `docs/`, and commit messages are explicitly *not* used as
  evidence. Stow claims below cite `path:line` in this repository.
- Sections split into **Facts** (sourced) and **Assessment** (my judgment, not sourced).
- No release numbers or version strings are asserted unless they appear verbatim in a cited
  first-party page. Where a project's release cadence is material but undocumented, I say so
  rather than guessing.
- **End-of-life is stated precisely or not at all.** "Repository unmaintained," "source-only
  distribution," and "commercial successor exists" are three different claims. This document never
  collapses them into "MinIO is dead," and §4.2 records the exact first-party wording.
- URLs are recorded as accessed; these documents change. Treat counts and coverage numbers as
  "as published on 2026-09-25."

---

## 1. Stow as built

Derived from source, in this repository.

### Facts

**Deployment model.** A single Go binary with one subcommand, `stow serve`
(`cmd/stow-s3/main.go:29-36`, `cmd/stow-s3/main.go:100-118`). No container, daemon supervisor, config
file, or license check is required to start it. It binds `127.0.0.1` by default and accepts
`--port 0` for an OS-assigned ephemeral port (`cmd/stow-s3/main.go:102`, `cmd/stow-s3/main.go:264`).

**Machine-readable readiness.** On successful bind, startup prints a single parseable line:
`STOW_READY endpoint=<host:port> access_key=<id> secret_key=<key> mode=<mode>`
(`cmd/stow-s3/main.go:273`). A supervising test harness or agent can block on readiness without
polling or scraping logs, and learns the endpoint *and* the generated credentials in one read.

**API surface.** Implemented S3 operations are exactly the methods on the `storage.Store`
interface plus their HTTP handlers (`internal/storage/store.go:9-32`): ListBuckets, CreateBucket,
HeadBucket, DeleteBucket, ListObjectsV2, PutObject, GetObject (with `Range` support,
`internal/s3api/handlers_multipart.go:214-278`), HeadObject, DeleteObject, DeleteObjects,
CopyObject, and the complete multipart set — create, upload part, complete, abort, list parts,
list multipart uploads (`internal/s3api/handlers_multipart.go:26-213`).

Explicitly *not* implemented, and rejected with `NotImplemented` when requested: versioning, ACL,
policy, lifecycle, replication, notification, tagging, website, logging, accelerate,
requestPayment, encryption, object-lock, inventory, metrics, analytics, intelligent-tiering, and
select (`internal/s3api/dispatch.go:121-128`). ListObjects **v1** is not served; only `list-type=2`
is routed (`internal/s3api/dispatch.go:52-57`). Browser `POST Object` form uploads are not
served. Bucket-policy-style subresources return `NotImplemented` explicitly
(`internal/s3api/dispatch.go:46-49`).

**Auth.** SigV4 for header-signed and presigned-query requests, enforced unconditionally
(`internal/auth/sigv4.go:16`, `internal/auth/sigv4.go:92-131`). The CLI always installs a SigV4
verifier (`cmd/stow-s3/main.go:224`, `cmd/stow-s3/main.go:237-250`); there is no flag to disable
signature checking. Exactly one credential pair is active per process
(`cmd/stow-s3/main.go:212-222`) — supplied via `--access-key`/`--secret-key`, via
`STOW_LOCAL_ACCESS_KEY_ID`/`STOW_LOCAL_SECRET_ACCESS_KEY` (`cmd/stow-s3/main.go:42-50`), or generated
at startup.

**Addressing.** Path-style by default; virtual-hosted-style available by passing `--base-host`,
which enables `bucket.<base-host>` host routing (`cmd/stow-s3/main.go:108`,
`internal/s3api/router.go:26-45`).

**Persistence.** Two selectable storage backends, `filesystem` (default) and `memory`
(`cmd/stow-s3/main.go:103-104`, `cmd/stow-s3/main.go:163-170`). The filesystem backend is a plain
directory, so a test's data is inspectable with ordinary filesystem tools.

**Resource bounds.** `--max-bytes` and `--max-objects` are enforced on every native S3 request
(`cmd/stow-s3/main.go:116-117`).

**Run-through mode.** A second operational mode, selected automatically when upstream endpoint and
credentials are present in the environment, or forced with `--mode run-through`
(`internal/runthrough/config.go:213-223`, `cmd/stow-s3/main.go:128-136`). It provides:
- read-through caching of upstream objects into a local store, with optional revalidation and
  eviction when the upstream object disappears (`internal/runthrough/config.go:38-45`,
  `internal/runthrough/config.go:105-110`);
- a bounded separate cache with `MaxBytes`, `MaxObjects`, and `TTL` limits
  (`internal/runthrough/config.go:38-45`, `internal/runthrough/config.go:165-201`);
- a write policy that is local-only by default, with `mirrorWrites` and opt-in live writes via
  `--allow-live-writes` / `STOW_ALLOW_LIVE_WRITES` (`internal/runthrough/config.go:20-25`,
  `cmd/stow-s3/main.go:68-73`, `cmd/stow-s3/main.go:224-230`);
- a durable file-backed outbox with prepared/terminal states, cross-process claims, and a
  background retry worker (`internal/runthrough/file_outbox.go`, `internal/runthrough/outbox.go`,
  `cmd/stow-s3/main.go:75-98`);
- upstream credential discovery with `STOW_*` > `S3_*` > `AWS_*` precedence
  (`internal/runthrough/config.go:60-99`).

**Admin and observability surface.** Loopback-restricted unless `--allow-public-admin` is set
(`cmd/stow-s3/main.go:109`, `cmd/stow-s3/main.go:138-140`). Endpoints: `/_stow/health`,
`/_stow/status`, `/_stow/inspect`, `/_stow/metrics` (Prometheus text exposition),
`/_stow/outbox/retry`, `/_stow/outbox/discard` (`internal/s3api/admin.go:50-94`). Metrics cover
cache hits/misses/evictions, active multipart uploads, and outbox depth and retry counts
(`internal/s3api/admin.go:320-365`).

**CORS.** A permissive fixed CORS policy with preflight handling, not S3-managed bucket CORS
configuration (`internal/s3api/cors.go:7-28`).

**Language and runtime model.** A Go 1.24 server, plus two additional consumption shapes in-tree:
- a Go in-process library at `pkg/stow` exposing `Open`, bucket/object operations, `Reset`,
  `Usage`, and `Capabilities` over an in-memory backend only (`pkg/stow/types.go:9-11`,
  `pkg/stow/runtime.go:15-128`);
- a Go→WASM build (`cmd/stow-wasm/main.go:1-3`) shipped in the npm package `@chs/stow-s3`, exposing an
  `embedded` in-process entrypoint plus `node-wasm` and `browser` hosts
  (`packages/stow-s3/package.json`).

**Session ergonomics.** The npm client spawns the binary itself, defaults to port `0`, parses the
`STOW_READY` line with a dedicated parser, and creates a temporary data directory it cleans up
(`packages/stow-s3/src/start.ts:8-17`, `packages/stow-s3/src/start.ts:291-315`). No container runtime is
involved at any point.

### Assessment

Stow is a **narrow, single-purpose test fixture with an escape hatch**, not a general-purpose S3
server. Its API surface is a curated subset that fails loudly (`NotImplemented`) rather than
silently approximating. That converts "does my code work against S3?" into a bounded question, and
converts "did the SDK misuse S3?" into an immediate, explicit failure instead of a mysterious wrong
answer.

---

## 2. Comparison set and why each tool is in or out

**Evaluated (as required):**

| Tool | Category |
| --- | --- |
| LocalStack | (1) local emulator for tests, (4) adjacent cloud emulator |
| MinIO (community edition) | (3) self-hosted S3-compatible server |
| s3rver | (2) lightweight/in-process S3 mock |
| moto | (2) lightweight/in-process S3 mock |
| SeaweedFS | (3) self-hosted S3-compatible server |
| Garage | (3) self-hosted S3-compatible server |

**Added, with justification from primary sources:**

- **S3Mock (Adobe)** — added to category (2). It is the only actively-maintained, first-party S3
  mock that is JVM-native and offers a Testcontainers/JUnit integration path
  (`https://github.com/adobe/S3Mock`, accessed 2026-09-25). Without it, category (2) would be
  represented only by a Python library and an archived Node project, which would overstate how
  thin that category is.

**Named but not evaluated, and why it matters:**

- **MinIO AIStor (Free and Enterprise)** — named, not evaluated as a competitor, because it is
  MinIO's own designated successor rather than an independent alternative. Its existence is the
  single most important fact in the MinIO-migration question, so it is treated in §4.3 and §8
  rather than in the fact sheets.

**Omitted, with justification:**

- **Ceph RADOS Gateway, OpenStack Swift, Riak CS, OpenIO** — omitted. These are production-scale
  object stores, not test tooling, and their S3 coverage is already tabulated against Garage in
  Garage's own compatibility matrix
  (`https://garagehq.deuxfleurs.fr/documentation/reference-manual/s3-compatibility/`, accessed
  2026-09-25). Including them would duplicate a first-party source rather than add signal.
- **Azurite, fake-gcs-server, and other non-S3 emulators** — omitted. They emulate different wire
  protocols; they clarify nothing about S3-specific positioning.
- **RustFS** — noted only. It is a Rust reimplementation of MinIO's storage model, named as such by
  SeaweedFS (`https://github.com/seaweedfs/seaweedfs`, "Compared to MinIO, RustFS" section,
  accessed 2026-09-25). Given §4, it deserves its own primary-source fact sheet before it belongs
  in this document; characterising it from a competitor's README would not be primary evidence.

---

## 3. Fact sheets

### 3.1 LocalStack

Sources (all accessed 2026-09-25):
`https://docs.localstack.cloud/aws/services/s3/`,
`https://docs.localstack.cloud/aws/getting-started/installation/`,
`https://docs.localstack.cloud/aws/licensing/`

**Facts.**

- **API compatibility.** Publishes a machine-readable coverage count: "100 of 116 operations
  implemented" for S3, and "32 of 97" for S3 Control, both annotated "Available from the Hobby
  plan." Coverage is also exposed as Markdown and JSON at stable paths under
  `/api-coverage/v1/services/`.
- Supported beyond the core data path: bucket CORS configuration, SSE-C *parameter* validation
  (explicitly, "LocalStack does not support the actual encryption and decryption of objects using
  SSE-C"), one-way and two-way S3 replication, and `ReplicationStatus` semantics.
- **Deployment model.** Container-first. Documented paths are the `lstk` CLI — for which a working
  Docker installation is a stated **requirement** — Docker Compose, `docker run`, Helm, and
  LocalStack Desktop. LocalStack for AWS requires an **Auth Token** to activate a running instance
  (`LOCALSTACK_AUTH_TOKEN` for Docker and CI; browser login plus system keyring for `lstk`).
- **Licensing is tier-gated, and this is material to the operating-cost comparison.** The
  licensing page states that as of 23 March 2026 the commercial subscriptions are Base, Ultimate,
  and Enterprise, with Hobby provided "for non-commercial use"; each license is assigned to an
  individual user and generates an auth token. In the published feature table:
  - *Local state persistence*: **Hobby ❌**, Base/Ultimate/Enterprise ✅.
  - *IAM Policy Enforcement*: **Hobby ❌**, Base+ ✅.
  - S3 and S3 Control: ✅ in every plan including Hobby.
  - *Testing in CI*: ✅ in every plan.
  - *Telemetry Sharing*: "enforced" on Hobby; "default on" on Base, Ultimate, Enterprise.
  - `lstk` authentication uses a browser login flow and stores credentials in the system keyring.
- **Persistence.** Local state persistence is off in Hobby and a paid feature in higher plans. The
  S3 service page carries an explicit "Persistence" support flag, and the compose examples mount a
  volume at `/var/lib/localstack` with `PERSISTENCE=${PERSISTENCE:-0}`.
- **Language/runtime model.** Python application on a container image; not embeddable in a host
  test process. Compose examples mount `/var/run/docker.sock` so services such as Lambda can start
  sibling containers.
- **Multi-tenancy/auth.** S3 signature validation is **off by default**, so "S3 accepts requests
  signed with any credentials." `S3_VALIDATE_SIGNATURES=1` enables SigV4 checking; presigned-URL
  checking is separately gated by `S3_SKIP_SIGNATURE_VALIDATION=0`. The documentation states
  validation "authenticates a request, but does not authorize it," with authorization handled by a
  separate, independently enabled engine — which the licensing table confirms is unavailable on
  Hobby. Real multi-account namespacing and IAM/STS-derived credentials are supported.
- **Addressing.** Virtual-hosted style is "the default and recommended approach" via
  `http://<bucket>.s3.localhost.localstack.cloud:4566`; path style is a documented fallback, and the
  docs call container-name addressing in Docker Compose "one of the most common reasons users need
  path-style requests."
- **Test/agent fit.** Strong for AWS-workload integration tests (S3 with Lambda, IAM, Step
  Functions together). Requires Docker and an activated instance before any S3 call can be made.

**Assessment.** LocalStack is a complement, not a substitute, for Stow's job. Its value appears
when the thing under test is a *cloud architecture* rather than an S3 client. Its costs are real
and should be stated precisely: a Docker daemon, a per-user auth token, DNS configuration for
virtual-hosted style, a mounted socket for some services, and — on the free tier — no local state
persistence and no IAM policy enforcement. Stow pays none of these. Conversely, a team that needs
persistence or authorization testing on a budget will find Hobby insufficient, which is a genuine
gap Stow happens to fill by a different route: Stow has no tiers.

### 3.2 MinIO (community edition)

Sources (all accessed 2026-09-25):
`https://github.com/minio/minio` (README, verbatim),
`https://docs.min.io/community/minio-object-store/index.html` (redirects into the AIStor
documentation tree at `https://docs.min.io/aistor/…`)

**Facts — stated exactly as published, without extrapolation.**

- **Repository status.** The README's first block is: "**THIS REPOSITORY IS NO LONGER
  MAINTAINED.**" It then names two alternatives: "**AIStor Free** — Full-featured, standalone
  edition for community use (free license)" and "**AIStor Enterprise** — Distributed edition with
  commercial support."
- **Distribution.** "**Important:** The MinIO community edition is now distributed as source code
  only. We will no longer provide pre-compiled binary releases for the community version."
  Installation is `go install github.com/minio/minio@latest` (minimum Go 1.24) or a self-built
  Docker image. Historical binaries remain downloadable but: "**These legacy binaries will not
  receive updates.** We strongly recommend using source builds for access to the latest features,
  bug fixes, and security updates."
- **What the README does *not* say.** It does not state a date. It does not say the project or
  company has ceased to exist, that running deployments stop working, that the AGPLv3 code is
  withdrawn, or that AIStor is deprecated. AIStor is presented as the forward path, and the
  AIStor documentation contains a dedicated "Upgrade from open-source MinIO" section with paths for
  Linux, Kubernetes, and airgapped environments.
- **License.** GNU AGPL v3. The README flags that "All usage of MinIO in your application stack
  requires validation against AGPLv3 obligations," including the release of modified code, and
  states that production use of compiled-from-source binaries "do so at their own risk" because
  "The AGPLv3 license provides no warranties nor liabilities for any such usage."
- **Deployment model.** `minio server PATH` against a local directory. Also Docker, Helm (a
  community-maintained chart in-repo, and the MinIO Operator), and bare metal.
- **Credentials.** "The MinIO deployment starts using default root credentials
  `minioadmin:minioadmin`." An embedded web console runs on a separate address; the README notes
  "MinIO runs console on random port by default," adjustable with `--console-address`. The `mc`
  client is the supported administration path.
- **Operational documentation.** Healthcheck probes, metrics (Prometheus and InfluxDB), audit
  logging, healing, and capacity expansion are all documented in the AIStor operations tree, which
  is where the open-source documentation URL now resolves. Erasure coding, node maintenance, and
  drive/node/site recovery are documented as operational concerns.
- **Auth and multi-tenancy.** Root plus IAM, LDAP, OIDC, Keycloak, Entra ID, Okta, and Kubernetes
  service-account identity, policy management, and a documented Multi-Tenancy administration
  section, all in the AIStor tree.
- **Adjacent claim, attributed.** SeaweedFS states: "as Apr 25, 2026 MinIO ceased development"
  (`https://github.com/seaweedfs/seaweedfs`, accessed 2026-09-25). I record this as a
  *competitor's* statement. The authoritative first-party evidence is MinIO's own "NO LONGER
  MAINTAINED" banner, which carries no date.

**Assessment.** Read precisely, three things happened and only three: the community repository
stopped receiving maintenance, the community build became source-only, and a commercially supported
successor was named. That is a *maintenance and distribution* change. It is not a data-format
change, not a wire-compatibility change, and not a statement that anyone's existing deployment is
now unsupported in practice. The practical consequences are real but narrow: a team that wants
community MinIO must now build it, and a team that wants a supported MinIO must buy AIStor. Stow
addresses neither.

### 3.3 s3rver

Source (accessed 2026-09-25): `https://github.com/jamhall/s3rver`

**Facts.**

- **Status.** "This repository was archived by the owner on Sep 14, 2025. It is now read-only."
  MIT licensed, 1,021 commits.
- **Self-description.** "S3rver is a lightweight server that responds to **some** of the same calls
  Amazon S3 responds to." The stated goal is "to minimise runtime dependencies and be more of a
  development tool to test S3 calls in your code rather than a production server looking to
  duplicate S3 functionality."
- **API compatibility.** Explicitly partial: create/delete/list buckets; list objects with prefix,
  delimiter, marker, max-keys, common prefixes; put object with metadata; post object (multipart);
  delete object(s); get object including HEAD; "Get dummy ACLs for an object"; copy object. Static
  website hosting is supported for incoming `GET`s via a website configuration file. No
  versioning, lifecycle, tagging, encryption, or replication is claimed.
- **Deployment model.** Three shapes: a global `s3rver` CLI; programmatically via
  `new S3rver(options).run(cb)`; and `s3rver.getMiddleware()` / `s3rver.callback()` for mounting
  inside an existing `http.createServer()` or Express app.
- **Persistence.** Optional `directory` option. `resetOnClose` (default `false`) removes all bucket
  data on server close.
- **Language/runtime model.** Node.js, zero framework, deliberately minimal dependencies.
- **Auth.** If the client only supports signed requests, use the fixed pair
  `accessKeyId: "S3RVER", secretAccessKey: "S3RVER"`. `allowMismatchedSignatures` (default `false`)
  "Prevent[s] `SignatureDoesNotMatch` errors for all well-formed signatures."
- **Disposable-session fit — strong.** Options include `silent`, `configureBuckets[]` for
  pre-provisioning buckets and their raw XML configs, and a `reset()` method: "Resets all bucket
  and configurations supported by the configured store."

**Assessment.** s3rver's *design* is the closest prior art to Stow's — lightweight, dev-only,
in-process-capable, disposable, no container. Its archival removes it as a forward-looking choice
and therefore removes a competitor from the exact segment Stow occupies. It also establishes that
the design space is real, which means Stow's differentiators must be the maintained ones: enforced
SigV4, quota bounds, an inspectable server-side state, and upstream run-through. Archival is not
the same as EOL-with-no-successor — s3rver has no named successor, which is the material
difference from MinIO.

### 3.4 moto

Sources (all accessed 2026-09-25):
`https://docs.getmoto.org/en/latest/docs/services/s3.html`,
`https://docs.getmoto.org/en/latest/docs/server_mode.html`

**Facts.**

- **API compatibility.** Publishes a per-operation `[X]`/`[ ]` checklist for S3. Implemented
  includes versioning, lifecycle, CORS, tagging, replication, ownership controls, bucket policy,
  notification configuration, `select_object_content`, object lock/retention/legal hold, and
  `upload_part` / `upload_part_copy`. The documentation is candid about limits: bucket policy
  enforcement considers "Only statements with principal=\*" and ignores conditions, described as
  "Basic policy enforcement"; `select_object_content` is "Highly experimental"; default `MaxKeys`
  is 100 rather than AWS's 1000; multipart minimum part size is 5 MB unless
  `S3_UPLOAD_PART_MIN_SIZE` is lowered; and `list_multipart_uploads` has no `delimiter` or
  `max-uploads`.
- **Deployment model.** Two modes. (a) In-process, as a Python library via `@mock_aws` decorators
  that intercept SDK calls. (b) A standalone `moto_server` process for any official AWS SDK,
  installed with `pip install moto[server]`, also available as `motoserver/moto` and
  `ghcr.io/getmoto/motoserver` Docker images, and via Homebrew. A `ThreadedMotoServer` can be
  started from inside Python on a background thread with `port=0` for a random free port; the
  decorators and the threaded server act on the same state.
- **Persistence.** The documented state lifecycle is reset-based: decorators destroy resources on
  start, and an internal reset API at `http://motoapi.amazonaws.com/moto-api/reset` clears all
  backends, with `MOTO_CALL_RESET_API=false` to retain state between tests. The server-mode
  documentation does not describe a durable on-disk store for backend state.
- **Language/runtime model.** Python. In-process mode is a library call inside a Python test;
  server mode is a separate process reached over HTTP.
- **Multi-tenancy/auth.** Account-aware: `MOTO_S3_ALLOW_CROSSACCOUNT_ACCESS` (cross-account access
  allowed by default) and per-account `S3Backend(region_name, account_id)` backends. Signature
  validation is not a documented feature of the mock surface.
- **Test/agent fit — strong.** A documented pytest fixture pattern using `port=0` and yielding an
  `endpoint_url`, a dashboard at `/moto-api/`, and `TEST_SERVER_MODE=true` to route decorator tests
  through the server so both approaches share state.
- **Disposable-session fit — strong, by construction.** The decorator or reset-API lifecycle is
  exactly a disposable per-test session.

**Assessment.** moto is the strongest pure test-tooling option in the landscape and Stow's closest
functional analogue by intent. Its asymmetry is language and deployment: the best in-process
experience is Python-only, and every other language gets a separate server process plus an
`endpoint_url` threaded through the test. Stow's analogue is a language-agnostic HTTP endpoint
whose port *and credentials* are handed to the harness on stdout. Where moto is weaker than Stow
is auth: it does not validate signatures, so it cannot catch an auth bug.

### 3.5 SeaweedFS

Source (accessed 2026-09-25): `https://github.com/seaweedfs/seaweedfs`

**Facts.**

- **License.** Apache-2.0. Go. Single `weed` binary.
- **API compatibility.** Publishes a table: "S3 bucket and object | 73", "S3 Tables | 36",
  "IAM | 39", "STS | 5". Claimed surface includes versioning, Object Lock with retention and legal
  hold, lifecycle rules, tagging, CORS, conditional reads and writes, checksums, presigned URLs,
  browser POST uploads, multipart uploads, and an atomic `RenameObject`. Also bucket policies
  *with* conditions and variables, IAM users/groups/policies, STS via OIDC, LDAP, and Kubernetes
  service accounts, and SSE-S3 / SSE-KMS / SSE-C with OpenBao, Vault, AWS KMS, Azure Key Vault, and
  GCP KMS as key providers. Audit log, bucket quota, and rate limiting are documented. The project
  states the S3 compatibility suite and SDK, IAM, SSE, policy, and Spark integration tests run in
  CI on every change.
- **Deployment model.** `weed mini -dir=./data` starts "a ready-to-use S3 object store" — S3 on
  port 8333, the named bucket created, the given credentials valid — in a single process that also
  runs master, a volume server, the filer, WebDAV, the Iceberg REST catalog, and the Admin UI.
  Also Docker, Docker Compose, and Helm. `weed mini` is "auto-tuned for one node and is fine for
  single-node production."
- **Persistence.** Filesystem-backed via `-dir`; the same path is used for Compose and Helm volumes.
  Filer metadata can be stored in SQLite or any of a long list of external stores.
- **Auth.** `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` environment variables, optionally
  disabled entirely: "Drop the AWS keys to run without authentication for development."
  `S3_BUCKET=my-bucket` pre-creates a bucket.
- **Upstream / run-through — present, via a different mechanism.** "Cloud Drive" mounts a bucket
  from S3, Google Cloud Storage, Azure, Backblaze B2, Wasabi, Storj, or any S3-compatible store and
  serves it locally: metadata pulled once so listing and stat cost no cloud API calls; content
  downloaded once on first read or warmed, cached with cluster capacity; "Local writes complete at
  local latency and are written back to the cloud asynchronously in the cloud's native layout, so
  other tools keep reading the bucket directly." A separate "Gateway to Remote Object Storage"
  mirrors every bucket to a remote store.
- **Operational complexity.** For `weed mini`, low. Beyond it: a master / volume / filer / S3
  topology with layout management, replication placement, and per-role persistent volumes.

**Assessment.** SeaweedFS is the only tool in this set that is both genuinely one-command to start
*and* a full production object store with a real IAM/STS surface. Its Cloud Drive capability is the
nearest analogue to Stow's run-through mode and is arguably broader in reach (whole-provider
mounting, explicit warm/uncache policy, cloud-native write-back layout). The trade is scale: these
are the same problem solved at very different sizes. A dev fixture should not require thinking
about volumes, filers, and layout assignment — but SeaweedFS is the tool that most successfully
argues it does not have to, which makes it Stow's most credible long-term competitor rather than
its nearest neighbour.

### 3.6 Garage

Sources (all accessed 2026-09-25):
`https://garagehq.deuxfleurs.fr/documentation/quick-start/`,
`https://garagehq.deuxfleurs.fr/documentation/reference-manual/s3-compatibility/`

**Facts.**

- **API compatibility.** Garage publishes its own compatibility matrix, with a disclaimer that it
  is informational, best-effort, and not proactively monitored. Implemented: SigV4, path-style and
  vhost-style URLs, presigned URLs, SSE-C, the core endpoints (CreateBucket, DeleteBucket,
  GetBucketLocation, HeadBucket, ListBuckets, HeadObject, CopyObject, DeleteObject, DeleteObjects,
  GetObject, **ListObjects v1**, ListObjectsV2, PostObject, PutObject), the **full multipart set
  including UploadPartCopy**, website endpoints (PutBucketWebsite partial), CORS, and a partial
  lifecycle (only `AbortIncompleteMultipartUpload` and `Expiration`).
  Missing: bucket versioning (GetBucketVersioning is a stub that always reports "versioning not
  enabled"), ListObjectVersions, all ACL and bucket-policy APIs, all replication APIs, all object
  lock APIs, all server-side-encryption APIs, notifications, and all tagging APIs. Garage
  "implements none" of the S3 ACL/policy mechanisms and instead "has its own system instead, built
  around a per-access-key-per-bucket logic."
- **Deployment model.** Single Rust binary, but **configuration-file-first**. A `garage.toml` must
  be written with `metadata_dir`, `data_dir`, `db_engine`, `replication_factor`, an `rpc_secret`,
  and separate bind addresses for S3, web, and admin. Default config path is `/etc/garage.toml`,
  overridable with `GARAGE_CONFIG_FILE`. `garage server --single-node --default-bucket` collapses
  cluster and credential setup for local use. The Docker example explicitly does not create
  volumes, so "your cluster will be wiped if the container terminates."
- **Persistence.** Filesystem (`metadata_dir`, `data_dir`) with `db_engine = "sqlite"` for a single
  node. The sample config points at `/tmp`, so "data stored on this Garage server will not be
  persistent" across reboots unless changed.
- **Auth.** Since v2.3.0, `GARAGE_DEFAULT_ACCESS_KEY`, `GARAGE_DEFAULT_SECRET_KEY`, and
  `GARAGE_DEFAULT_BUCKET` are read from the environment to auto-create a key and bucket. The admin
  API on port 3903 is gated by `admin_token`; metrics by a separate `metrics_token`.
- **Operational complexity.** Four listening ports: RPC 3901, S3 3900, web 3902, admin 3903. The
  quick start is a 9-minute tutorial and explicitly warns that a single-node deployment "should not
  be used in production, as it provides no redundancy for your data!" Full manual cluster
  configuration remains documented alongside the convenience flags.
- **Test/agent fit.** Weak relative to peers. No documented ephemeral-port mode, no
  machine-readable readiness line, and no documented in-process embedding.
- **Disposable-session fit.** Not a documented workflow. Teardown is process kill; a per-test
  `data_dir` is the only isolation primitive implied.

**Assessment.** Garage is the most honest minimal S3 server in the set — its compatibility matrix
documents what is missing rather than leaving users to discover it. It is also a distributed object
store being run on one node, and the configuration-file-first model is friction a harness pays on
every invocation.

### 3.7 S3Mock (Adobe)

Source (accessed 2026-09-25): `https://github.com/adobe/S3Mock`

**Facts.**

- **API compatibility.** "A lightweight server that implements a subset of the Amazon S3 API for
  local integration testing," with a per-operation support table linked against the AWS operations
  reference, and a **Version Compatibility** table (5.x active on Spring Boot 4.0.x / Kotlin 2.3 /
  Java 17 target; 4.x deprecated; 3.x, 2.x, 1.x end-of-life). Basic versioning was added in 4.x.
  It also carries *experimental* support for the S3 Vectors API on separate ports behind the
  `vectors` Spring profile.
- **Documented limitations.** "**Path-style access only**: S3Mock supports
  `http://localhost:9090/bucket/key`, not `http://bucket.localhost:9090/key`." Presigned URLs are
  "accepted but not validated (expiration, signature, HTTP verb not checked)". KMS key ARNs are
  "validated, but no actual encryption is performed". "**Not for production**: S3Mock is a testing
  tool and lacks the security features required for production use."
- **Deployment model.** Four options: Docker (the README's *recommended* usage, "to avoid classpath
  conflicts"), a Testcontainers `S3MockContainer`, a JUnit 5 extension, and a TestNG listener — with
  the maintainers' own signal that the JUnit 5 and TestNG modules "may be removed in S3Mock 6.x.
  Consider using Testcontainers instead."
- **Persistence.** Filesystem store rooted at `COM_ADOBE_TESTING_S3MOCK_STORE_ROOT` (default: a
  Java temp directory). "By default, S3Mock deletes all stored data on shutdown. Set
  `COM_ADOBE_TESTING_S3MOCK_STORE_RETAIN_FILES_ON_EXIT=true` only when you need data to survive
  restarts." Reusing persisted data across restarts is "not officially supported."
- **Language/runtime model.** Kotlin on Spring Boot 4.x, JVM bytecode target 17, built with Java 25
  and Maven 3.9+. Apache-2.0. OpenSSF Best Practices and Scorecard badges present.
- **Auth.** No signature validation, as implied by the presigned-URL limitation. Example clients
  use static `foo`/`bar` credentials.
- **Test/agent fit — good, JVM-only.** A ready-made Testcontainers module with
  `withInitialBuckets(...)` and `httpEndpoint`. A dedicated readiness endpoint exists at
  `/favicon.ico` returning `200 OK` with an empty body, "used by Testcontainers and the integration
  test suite to detect readiness." A `debug` Spring profile enables Actuator.
- **Disposable-session fit — good.** Delete-on-shutdown plus Testcontainers lifecycle gives a clean
  per-session store.
- **Seed/inspect fit.** `COM_ADOBE_TESTING_S3MOCK_STORE_INITIAL_BUCKETS` creates buckets on startup,
  and the documented on-disk layout (`<root>/<bucket>/<object-uuid>/binaryData` plus
  `objectMetadata.json`) is browsable, though the README notes it is an implementation detail that
  "may change between releases."

**Assessment.** S3Mock is the best-engineered test fixture in category (2): a real readiness probe,
a first-class container path, a published limitations list, and a compatibility matrix that admits
its own deprecations. It is JVM-bound by construction, and it validates almost nothing — a suite
using it exercises request routing and error shapes, not authentication.

---

## 4. Testing the wedge: does "built in response to MinIO's EOL" hold up?

This section exists to stress-test a hypothesis, not to assert it. The hypothesis has three parts.
I test each separately because they have very different levels of support.

*Stated project intent, recorded as such:* Stow was built partly in response to MinIO's
end-of-maintenance status, targeting modern developer experience and no Docker overhead. **This is
stated intent from the product owner and is not independently verifiable from the code** — nothing
in the source records a motivation. What follows tests the three sub-claims against primary
sources and against Stow's own code.

### 4.1 Claim A — "no Docker overhead": **fully supported**

| Tool | Container required to obtain a usable S3 endpoint? | Source |
| --- | --- | --- |
| **Stow** | **No.** One binary, `stow serve`. The npm client spawns it directly. | `cmd/stow-s3/main.go:29-36`; `packages/stow-s3/src/start.ts:315` |
| LocalStack | **Yes.** Docker is a stated requirement for the `lstk` path; all paths are container-based. | `https://docs.localstack.cloud/aws/getting-started/installation/` |
| S3Mock | Docker *or* JVM. The README's recommended usage is Docker. | `https://github.com/adobe/S3Mock` |
| moto | **No** for Python in-process; Docker only for the standalone server. | `https://docs.getmoto.org/en/latest/docs/server_mode.html` |
| s3rver | **No.** `npm install`, or in-process. *(Archived.)* | `https://github.com/jamhall/s3rver` |
| SeaweedFS | **No.** `weed mini -dir=./data` is a single process. | `https://github.com/seaweedfs/seaweedfs` |
| Garage | **No.** Single binary, Docker optional. | `https://garagehq.deuxfleurs.fr/documentation/quick-start/` |
| MinIO (community) | **No** to run — but **yes** to obtain: source-only distribution means `go install` or a self-built image. | `https://github.com/minio/minio` |

**Assessment.** Claim A is true of Stow, but it is **not a differentiator against the field** —
it is a differentiator against exactly two tools, LocalStack and MinIO-community. moto, s3rver,
SeaweedFS, and Garage all start without Docker too. The honest framing is that Stow is in the
majority on this axis and beats the two tools a team is most likely to reach for first: LocalStack
for familiarity, and a Docker Hub MinIO image for convenience. Note the MinIO nuance: the
constraint moved from "must run a container" to "must compile a Go server," which is worse for a
test fixture, not better.

### 4.2 Claim B — "response to MinIO's EOL": **supported at the category level, not as a migration claim**

What the primary sources actually establish, in three separable facts:

1. **The community repository is not maintained.** MinIO's own README: "**THIS REPOSITORY IS NO
   LONGER MAINTAINED.**" (`https://github.com/minio/minio`, accessed 2026-09-25).
2. **The community build is source-only.** "The MinIO community edition is now distributed as
   source code only. We will no longer provide pre-compiled binary releases." Legacy binaries
   "will not receive updates." (Same source.)
3. **A commercially supported successor exists and is designated for upgrades.** The README names
   AIStor Free and AIStor Enterprise as the alternatives, and the AIStor documentation tree
   contains an "Upgrade from open-source MinIO" section with Linux, Kubernetes, and airgapped
   paths (`https://docs.min.io/aistor/administration/upgrade-aistor-server/open-source-minio/`,
   reached via `https://docs.min.io/community/minio-object-store/index.html`, accessed
   2026-09-25).

What those three facts do **not** establish, and what this document therefore does not claim:

- That MinIO's S3 wire API changed. Nothing in the cited sources indicates it did.
- That existing self-hosted MinIO deployments stopped working or lost their upgrade path. The
  opposite is documented: MinIO publishes upgrade paths into AIStor.
- That "community MinIO" is end-of-life as a *product line*. It is the *repository* that is
  unmaintained, and the *distribution* that changed. The company and a successor edition are
  active.
- Any date. MinIO's README carries no date; the "Apr 25, 2026" figure is SeaweedFS's claim about
  a competitor and is not treated as fact here.

**Assessment.** The causal story is credible in a specific, narrow form: **the dev-fixture
category lost its default incumbent.** For years the reflexive answer to "I need an S3 endpoint in
my tests" was "run MinIO in Docker," and that reflex is now coupled to a repository that will not
take fixes and a distribution that must be built. That is a real opening, and it is plausibly why
Stow was worth building now. But it is a *timing* argument, not a *displacement* argument. Nobody
is switching from community MinIO to Stow, because the people running community MinIO in
production are not the people spawning a throwaway bucket per test, and the people spawning a
throwaway bucket per test were probably using LocalStack, moto, or a Docker MinIO image — not a
source build of an AGPL server they now have to compile.

### 4.3 Claim C — "modern developer experience": **supported, with a specific and narrow meaning**

Supported by Stow's code, and each item is verifiable:

- Machine-readable readiness carrying endpoint *and* credentials on stdout
  (`cmd/stow-s3/main.go:273`), parsed by a dedicated client-side parser
  (`packages/stow-s3/src/start.ts:8-17`).
- Ephemeral ports by default in the shipped client (`packages/stow-s3/src/start.ts:300`), so parallel
  workers never collide.
- Bounded sessions: memory backend (`cmd/stow-s3/main.go:167`), `Reset()` in the Go library
  (`pkg/stow/runtime.go:106-108`), temp-directory cleanup in the npm client
  (`packages/stow-s3/src/start.ts:1`), and per-instance storage quotas
  (`cmd/stow-s3/main.go:116-117`).
- Server-side state inspectable for assertions: `/_stow/inspect`, `/_stow/status`, `/_stow/metrics`
  (`internal/s3api/admin.go:50-94`).
- Three consumption shapes from one implementation: subprocess, Go in-process library, and WASM
  embedded in Node or a browser (`cmd/stow-wasm/main.go:1-3`, `packages/stow-s3/package.json`).

Where the claim does **not** hold, and the document should not imply otherwise:

- **It is not a claim about API currency.** Stow's surface is a strict subset, and eleven named S3
  feature families are rejected with `NotImplemented` (`internal/s3api/dispatch.go:121-128`).
  Compared with moto's checklist or SeaweedFS's 73 S3 operations, Stow is behind on coverage by a
  wide margin. "Modern DX" here means *how you start and tear it down*, not *what it can do*.
- **It is not a claim about auth realism.** Stow's SigV4 is real and always on, but it is a single
  credential pair with no users, roles, policies, or STS
  (`cmd/stow-s3/main.go:212-222`). LocalStack, SeaweedFS, and AIStor all model multi-principal
  authorization. Stow can prove a client *signs* correctly; it cannot prove a client is
  *authorized* correctly.

**Assessment.** Claim C is the strongest and most defensible of the three, provided it is scoped to
session lifecycle. That is also the claim most likely to be mis-sold: "modern developer experience"
read by a prospective user as "modern S3 implementation" would be a promise the code does not keep.

---

## 5. Cost of operating, compared

Cost here means what a team must own, install, configure, license, and tear down to get a working
S3 endpoint in a test or agent loop. Every cell below is sourced; the ranking commentary that
follows is judgment.

| Cost dimension | Stow | LocalStack | MinIO (community) | s3rver | moto | SeaweedFS | Garage | S3Mock |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| **Host prerequisite** | None beyond the binary | **Docker required** | **Go toolchain to build**, or a self-built image | Node.js | Python (server mode) | None | None | JVM (or Docker) |
| **Install step** | Download binary | `lstk` / image pull | `go install` or `docker build` | `npm install` | `pip install moto[server]` | Download `weed` | Download `garage` | Image pull or Maven dep |
| **License / auth gate** | None | **Per-user auth token required to activate**; Hobby = non-commercial only | AGPLv3 obligations; paid AIStor for support | None (MIT) | None (Apache-2.0) | None (Apache-2.0) | None | None (Apache-2.0) |
| **Config file** | **None** | Compose/Helm optional | None | None | None | None | **`garage.toml` required** | None |
| **Fixed ports** | **No** — `--port 0` | 4566 + 4510-4559 + 443 | 9000 + console | 4568 | 5000 (configurable) | 8333 | 3900/3901/3902/3903 | 9090 / 9191 |
| **Machine-readable readiness** | **`STOW_READY` on stdout** | No documented equivalent | Healthcheck probes documented; credential+endpoint handshake not verified | No documented equivalent | `port=0` + `get_host_and_port()` | No documented equivalent | No documented equivalent | `/favicon.ico` → `200` |
| **Local state persistence** | `filesystem` or `memory` | **Hobby ❌; paid plans ✅** | Yes (server runs on a data path) | Optional `directory` | Reset-based; no durable store documented | Yes (`-dir`) | Yes, but sample config points at `/tmp` | **Deletes on shutdown by default** |
| **Per-instance storage bound** | **`--max-bytes` / `--max-objects`** | Not documented | Bucket quotas documented in AIStor tree | Not documented | Not documented | Bucket quota documented | Not documented | Not documented |
| **In-process embedding** | **Yes** — Go lib + WASM | No | No | **Yes** — `getMiddleware()` | **Yes** — Python decorator | No | No | Yes — JUnit 5 extension *(may be removed in 6.x)* |
| **Teardown primitive** | Process exit + temp dir cleanup | Container teardown | Process exit + throwaway path | `reset()` / `resetOnClose` | Decorator scope / reset API | Process exit + throwaway `-dir` | Process exit | Container lifecycle |
| **Maintenance status** | This repo | Commercially maintained | **Repo unmaintained; source-only; AIStor successor** | **Archived 2025-09-14, read-only, no successor named** | Actively maintained | Actively maintained | Actively maintained | Actively maintained; 5.x active |

### Assessment

**Lowest total cost to operate: Stow, then s3rver's design (moot while archived), then moto,
Garage, SeaweedFS `weed mini`, S3Mock, MinIO-community, LocalStack.**

The ordering deserves its qualifiers, because a single ranking hides two different jobs:

- **For a single test, in a language-agnostic harness:** Stow wins on prerequisites (no Docker, no
  config file, no license), on collision-freedom (ephemeral ports), and on blast radius
  (`--max-bytes`). The npm client makes this a one-liner. Nothing else in the field combines all
  three.
- **For a Python test suite:** moto's in-process decorator is genuinely cheaper — no subprocess, no
  port, no endpoint-URL plumbing. Stow does not win its own category's native ground here, and
  should not pretend to.
- **For a JVM test suite:** S3Mock with Testcontainers is competitive and better-maintained-feeling
  than it has any right to be, though it requires a container and validates no signatures.
- **LocalStack is the most expensive to operate and the least substitutable.** It is the only tool
  that costs you a Docker daemon *and* a license token *and* a DNS decision, and on the free tier
  it withholds both local state persistence and IAM policy enforcement. That is a defensible
  business model, not a flaw — but it means the "just use LocalStack" advice has a real, and
  occasionally disqualifying, price.
- **MinIO-community has inverted its cost curve.** It used to be the cheapest credible S3 endpoint
  (one `docker run`). It is now the second-most expensive to obtain, because source-only
  distribution puts a compiler in the path of every developer and every CI job that wants it. The
  AGPL review is a real, separate, non-technical cost that some teams cannot absorb at all.
- **Garage's cost is concentrated in ceremony, not capability.** A required `garage.toml`, an
  `rpc_secret` to generate, four ports, and an admin token is a lot of setup for a fixture, even
  though the resulting server is good.
- **SeaweedFS `weed mini` is the strongest counter-example to Stow's own thesis**, and should be
  treated as such. It is one process, one flag, no config file, no license, and a much larger API
  surface. The only things Stow has over it are enforced SigV4, quota bounds, the `STOW_READY`
  handshake, and genuine in-process/WASM embedding. That is a thinner margin than the positioning
  in §6 might suggest, and it is worth watching.

---

## 6. Comparison matrix

`—` = not applicable. `No` = the primary source documents the absence.

| | **Stow** (this repo) | **LocalStack** | **MinIO** (community) | **s3rver** | **moto** | **SeaweedFS** | **Garage** | **S3Mock** (Adobe) |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| **Category** | Fixture + run-through | Cloud emulator | Self-hosted server | In-process mock | In-process mock + server | Self-hosted server | Self-hosted server | In-process mock |
| **Target job** | Per-test / per-agent session | Whole-AWS integration testing | Self-hosted production storage | Sandbox testing | Per-test mocking | Self-hosted production storage + cache | Small-cluster self-hosting | Per-test integration testing |
| **API surface** | Deliberate subset; explicit `NotImplemented` | 100/116 S3 ops; 32/97 S3 Control | Broad; full S3 surface, product-documented | "some" calls; no versioning/lifecycle/tagging | Broadest mock coverage; versioning, lifecycle, tags, policy, lock | 73 S3 bucket/object ops + 36 S3 Tables + 39 IAM + 5 STS | Core + full multipart + presigned + SSE-C; no versioning/ACL/policy/lock/SSE/tagging | Subset; basic versioning; experimental S3 Vectors |
| **Deployment** | Single binary, `stow serve`, no config file | Docker-first (`lstk`/Compose/Helm) + auth token | Binary, now **source-only build** | CLI, programmatic, or Express middleware | Python lib or `moto_server` process/Docker | `weed mini` one command; Docker/Compose/Helm | Config file + binary/Docker; 4 ports | Docker (recommended), Testcontainers, JUnit 5 |
| **Persistence** | `filesystem` (default) or `memory` | **Hobby ❌**; paid plans ✅ | Filesystem (server runs on a data path) | Optional `directory`; `resetOnClose` | Reset-based; no durable store documented | Filesystem (`-dir`); SQLite/external filer store | Filesystem + SQLite metadata | Filesystem; **deletes data on shutdown by default** |
| **Language / runtime** | Go binary + Go lib + Go→WASM (Node/browser) | Python in container | Go (must be compiled) | Node.js | Python | Go | Rust | Kotlin / JVM (Java 17) |
| **SigV4 enforced** | **Yes, always on**, incl. presigned | No by default; opt-in; authz separate and paid on Base+ | Yes | No (fixed `S3RVER` creds) | Not a documented feature | Yes (unless keys dropped) | Yes (SigV4) | No — presigned not validated |
| **Multi-tenancy / authz** | Single credential pair; single tenant | IAM + STS + multi-account; authz engine paid on Base+ | Root + IAM/LDAP/OIDC/STS; multi-tenancy documented | One fixed pair | Account-scoped backends; basic policy (stated limits) | Full IAM + STS (OIDC/LDAP/K8s SA) | Per-key-per-bucket own model; no S3 ACL/policy APIs | None |
| **Test/agent fit** | `STOW_READY` handshake; `/_stow/*` admin + Prometheus; `--port 0`; quotas | Container lifecycle; Resource Browser; strong for multi-service AWS tests | `mc` + embedded console; heavyweight for a fixture | `silent`, `configureBuckets`, `reset()` | pytest fixture with `port=0`; `/moto-api/` dashboard + reset API | `weed mini`; admin UI; Prometheus | CLI-driven; no readiness line; no ephemeral-port mode | `/favicon.ico` readiness; Testcontainers module; initial-bucket seeding |
| **Operational complexity** | **Lowest**: one process, no deps, no config, no license | **Highest**: Docker + auth token + DNS/TLS + socket mounts; tier-gated features | **High to obtain**: source build; AGPL review | **Low**: one npm package, in-process possible | Low for Python; medium for other languages | **Low for `weed mini`**; high beyond it | Medium: config file, 4 ports, cluster concepts | Low: one container |
| **Upstream / run-through** | **Yes, first-class**: read-through cache, mirror/live write policies, durable outbox with retry + inspect | No (inherently terminal) | No (terminal) | No | No | **Yes**: Cloud Drive mount + async write-back; remote mirror gateway | No | No |
| **Disposable session** | **Yes**: memory backend, `Reset()` in lib, temp dir cleanup in npm client | Yes, via container teardown | Yes, via a throwaway data dir | Yes, via `resetOnClose` / `reset()` | Yes, via decorators or reset API | Yes, via throwaway `-dir` | Implied only (per-test `data_dir`) | Yes, via container lifecycle + delete-on-shutdown |
| **Maintenance** | This repo | Commercially maintained | **Repo unmaintained; source-only; AIStor successor** | **Archived, read-only, no successor named** | Active | Active | Active | Active (5.x) |

---

## 7. Stow positioning: replacement or complement?

### Facts about the competitive set

- The self-hosted S3 server market retains MinIO's position via AIStor, with documented upgrade
  paths from open-source MinIO, and adds actively-maintained alternatives (SeaweedFS, Garage,
  RustFS) with broad or well-documented S3 coverage.
- The test-fixture market retains strong, actively-maintained, zero-license options: moto, S3Mock,
  and `weed mini`; it lost its most reflexive default (a Docker MinIO image) to a distribution
  change; and LocalStack remains available but is container- and licence-gated.
- The upstream-aware category has exactly two credible occupants: SeaweedFS Cloud Drive and Stow
  run-through.

### Assessment

**Stow is a complement, not a replacement, and it should be positioned that way.** The reasoning,
tool by tool:

- **It is not a MinIO replacement, and cannot be.** Stow stores objects in a local directory or in
  memory (`cmd/stow-s3/main.go:163-170`). It has no versioning, no lifecycle, no encryption, no
  replication, no erasure coding, no healing, no multi-tenancy, no admin UI, and one credential
  pair. A team with a self-hosted MinIO deployment has a durability, availability, and data-growth
  problem; Stow has none of the machinery for any of those, and never claimed to. The right
  answer to such a team is MinIO's own: **AIStor**, or a maintained alternative such as SeaweedFS
  or Garage. Recommending Stow to them would be a category error, and the AGPL question their
  legal team is already asking has nothing to do with Stow.
- **It is not a LocalStack replacement.** If the test exercises S3 together with Lambda, IAM, or
  Step Functions, LocalStack is the only evaluated tool that does that, and it does more than Stow
  by a wide margin on both S3 and non-S3 services. Stow has no story here.
- **It is not a moto replacement for Python.** moto's in-process decorator is cheaper and deeper
  for a Python suite. Stow's advantages — real SigV4, quotas, sub-second process startup, WASM
  embedding — are advantages *over the wire*, which is precisely what a Python in-process mock does
  not need.
- **It is not an S3Mock replacement for the JVM**, for the same reason plus a JVM one.
- **It is not a SeaweedFS replacement.** Different sizes, different jobs. `weed mini` is a serious
  alternative to Stow for a *sandbox* and loses only on auth enforcement, quotas, and embedding.
- **It is not a Garage replacement.** Garage does more; it costs more ceremony. For a team that
  wants a real small object store for staging, Garage is the better buy.
- **It *is* the differentiated option in one narrow place:** a per-session, quota-bounded,
  SigV4-enforcing, in-process-capable endpoint that can optionally front a real bucket with a
  controlled write blast radius. No other evaluated tool offers that combination.

**The complementary relationships are the commercially interesting ones.** The realistic
compositions are:

1. **Stow in place of LocalStack** for the S3-only portion of a test suite that never touches
   another AWS service. This is the largest realistic win, because it is the case where LocalStack's
   Docker and licence overhead is pure tax.
2. **Stow in front of a real bucket** (run-through, local-only writes) as a conformance harness:
   run the test suite against Stow, then re-point the same suite at the production bucket with
   `--allow-live-writes` to confirm behaviour under real semantics, with a bucket-scoped prefix as
   the only concession. The durable, inspectable outbox
   (`internal/runthrough/file_outbox.go`, `internal/s3api/admin.go:77-88`) is what makes the second
   run safe to automate rather than terrifying.
3. **Stow as an agent's S3 substrate** — per-task, ephemeral, inspectable, self-cleaning. Nothing
   else in the field is shaped for this.
4. **Stow alongside, not instead of, a real store** in staging: a fixture for unit tests, Garage or
   SeaweedFS for the staging environment. These are not substitutes and should not be sold as one.

**The honest market.** Stow's realistic buyers are not "teams choosing an S3 emulator." They are
teams that concluded LocalStack is too much machinery *and* a production store is too much
durability *and* their language's native mock does not exercise the wire. That is a real but narrow
wedge, and its defensibility rests on the session-lifecycle engineering, not on the category
opening that MinIO's distribution change created. **The MinIO situation is a reason Stow was worth
building now. It is not a reason to buy it.**

---

## 8. What evidence a MinIO-migration claim would require

Stow cannot currently support a "migrate off MinIO to Stow" claim, and no amount of marketing copy
will change that — the capability gap in §7 is structural. If a migration-adjacent claim is ever
to be made, it must be one of the three narrower claims below, each with its own evidence bar.
Stow satisfies **none** of these today.

### Claim 1 — "Migrate your *test suite* off your MinIO container to Stow"

This is the only defensible version, because it is about test infrastructure, not storage.

*Currently supported?* Partially, and the blocker is narrow. Stow's API subset is a strict
superset of "S3 object CRUD + multipart + range + copy + delete-many"
(`internal/storage/store.go:9-32`), which is what most application test suites touch. But
ListObjects v1 is not served (`internal/s3api/dispatch.go:52-57`) and eleven feature families are
rejected (`internal/s3api/dispatch.go:121-128`), so a suite exercising tagging, presigned POST
uploads, or lifecycle will break.

*Evidence required:*
1. **A documented subset contract** naming exactly which operations are supported, so a migration
   guide can be written against it. Today the contract is implicit in the code.
2. **A runnable worked example**: a real application's test suite, running against MinIO today and
   Stow after, with the diff in test code shown. Not a toy.
3. **A failure-mode matrix**: what breaks, and what the migration path is for each break.
4. **Measured evidence** of the two costs being compared — cold start and teardown per test, CI
   wall-clock, and local memory — for MinIO-in-Docker and for Stow, on the same suite. Without
   numbers this is a preference, not a claim.
5. **Concurrency evidence**: N parallel workers, showing no port or state collisions
   (`packages/stow-s3/src/start.ts:300` implies this; nothing published demonstrates it).
6. **Migration guidance for the vhost-style addressing difference**, since MinIO and LocalStack
   default to vhost-style and Stow defaults to path-style with `--base-host` opt-in
   (`internal/s3api/router.go:26-45`).
7. **A credential-migration note**: MinIO defaults to `minioadmin:minioadmin` and clients will have
   that baked in; Stow generates a random pair per process. The client change is small but must be
   documented.

### Claim 2 — "Stow is the MinIO-compatible S3 layer for your pipeline"

*Currently supported?* No. This is a production-storage claim and Stow fails it outright: local
filesystem or in-memory only (`cmd/stow-s3/main.go:163-170`), no versioning, no lifecycle, no
encryption, no replication, no healing, no multi-tenancy, no HA, no admin UI
(`internal/s3api/dispatch.go:121-128`, `cmd/stow-s3/main.go:212-222`).

*Evidence required:* everything in Claim 1, **plus** durability guarantees under process and
machine failure, a documented storage-growth story, backup and restore, an access-control model
with more than one principal, encryption at rest, an operational runbook, and an HA story. That is
a multi-year programme, and it is the programme SeaweedFS, Garage, or AIStor have already run. The
honest conclusion is that Stow should not pursue this claim.

### Claim 3 — "Stow is the MinIO-compatible answer for self-hosted storage after MinIO's exit"

*Currently supported?* No, and the premise belongs to someone else. The primary source names the
answer: MinIO's README points to AIStor Free and AIStor Enterprise, and the AIStor documentation
publishes "Upgrade from open-source MinIO" procedures for Linux, Kubernetes, and airgapped
deployments. **The vendor has already told affected users what to do, and it is not Stow.**

*Evidence required to compete for this claim at all:* the full durability and operational
programme in Claim 2, an on-disk format and migration path from MinIO's erasure-coded layout, a
documented compatibility matrix against MinIO's S3 behaviour, and — decisively — evidence that
someone other than Stow's authors considers Stow fit to hold production data. Nothing in this
repository suggests that, and the correct conclusion is that the market has already been answered
by better-qualified parties.

### The one claim that is supportable today

Not a MinIO claim, but the adjacent one that the evidence *does* support: **for S3-only test and
agent workloads, Stow removes the Docker dependency, the licence dependency, and the fixed-port
collision problem, while adding enforced signature validation, per-instance storage bounds, and
server-side state inspection that no other evaluated tool combines.** That is Claim 1, minus the
migration framing — a session-cost argument, not a storage-migration argument. It should be
marketed as such.

---

## 9. Caveats

1. **Single access date.** Every external URL was retrieved on 2026-09-25. LocalStack's API
   coverage counts and licensing table, moto's feature checklist, and S3Mock's version table are
   live documents and will drift. Re-verify before relying on any specific number.
2. **No version pinning.** No release numbers are asserted for any competitor except where a cited
   page prints one (Garage `v2.3.0` in its Docker example, S3Mock's 5.x/4.x/3.x rows). This
   document cannot tell you which competitor is on its newest release.
3. **MinIO's status is stated in three separable facts, not one.** Repository unmaintained +
   source-only community distribution + a named commercial successor (AIStor) with published
   upgrade paths. There is no first-party date, and the "Apr 25, 2026" figure circulating
   elsewhere is SeaweedFS's claim about a competitor, not MinIO's statement. Do not cite a MinIO
   end-of-life date to this document, and do not describe MinIO as dead, discontinued, or
   incompatible. Existing deployments are not documented as broken.
4. **s3rver's archival is not an EOL-with-no-successor.** The repository is read-only since
   2025-09-14 and no successor is named, which is the material difference from MinIO. It is still
   the closest design prior art to Stow, and it is still installable and working for its documented
   subset.
5. **LocalStack's tier gating is time-sensitive.** The licensing page is dated "As of March 23rd,
   2026" and includes a legacy-plan section. The specific gating of local state persistence and IAM
   policy enforcement to paid plans may change; re-check before quoting it in a commercial context.
6. **Garage's compatibility matrix is a self-assessment.** Its own disclaimer states entries are
   "informational," "best effort," sometimes sourced from source code, and that maintainers are
   "not proactively monitoring new versions." Its update history ends at 2022-05-25, so the table
   may lag the implementation.
7. **SeaweedFS's operation counts are self-reported** in its README and are not reconciled against
   a published AWS operations list. Its comparison of itself to MinIO is, obviously, interested.
8. **Coverage figures are not comparable across tools.** "100 of 116" (LocalStack) counts against
   AWS's S3 operation list; SeaweedFS's "73" is a raw number the project chose; moto's list is
   `[X]`/`[ ]` against boto3 operations; Garage's is a hand-maintained table. Only LocalStack's is
   machine-readable and versioned. Treat cross-tool API comparisons as directional.
9. **Stow's scope was read from source, not docs.** Since `README.md` and `docs/` were excluded as
   evidence by instruction, any divergence between this document and the project's own
   documentation is resolved *in favour of the code*. If the code and docs disagree, the code is
   what this document describes.
10. **Stated project intent is not evidence.** §4's opening premise — that Stow was built partly in
    response to MinIO's status — is recorded as stated intent from the product owner. No source in
    this repository records a motivation, and §4 tests the three sub-claims against primary sources
    rather than assuming them.
11. **Absent documentation is not absent capability.** Where this document says a tool does not
    document durable persistence (moto), a readiness handshake (Garage, s3rver, SeaweedFS), or an
    ephemeral-port mode (Garage, SeaweedFS), that is a statement about the first-party
    documentation as of 2026-09-25. MinIO and AIStor *do* document healthcheck probes; Stow's
    advantage is the specific combination of a readiness line that also carries credentials, not the
    existence of a health endpoint. I did not run any of these tools, and this document reports no
    empirical test results.
12. **No benchmark or performance comparison is offered.** Every tool publishes throughput figures
    on its own hardware; none are comparable, and Stow's numbers were not measured for this
    document. The "cost of operating" comparison in §5 is about prerequisites, licensing, and
    ceremony — not speed. Claims about relative speed are deliberately absent.
13. **Scope discipline.** AIStor is named and treated in §4 and §8 but not given a fact sheet,
    because it is MinIO's successor rather than an independent alternative. RustFS is named but not
    evaluated. Both should be added on their own primary sources before this document is used to
    support any claim about self-hosted storage.
