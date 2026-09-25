# Session performance baseline

**Status:** recorded
**Date:** 2026-09-25
**Purpose:** give the targets in `docs/agent-dx-plan.md` section 11 a measured
starting point, and size the default session quotas from data.

Reproduce with:

```sh
make build
node packages/stow-s3/scripts/benchmark-session.mjs --sessions 30 --payload-bytes 1048576
node packages/stow-s3/scripts/benchmark-session.mjs --sweep
```

Results are written to `docs/benchmarks/session-baseline.json`. The harness is a
measurement tool and is deliberately **not** wired into `make standards`,
because a wall-clock gate on shared CI would be flaky.

## Environment

| | |
|---|---|
| CPU | Intel Core i5-7600K @ 3.80 GHz, 4 cores |
| Memory | 15,887 MB |
| OS | Linux x64, kernel 7.0.0-34-generic |
| Node | v24.14.0 |
| Backend | memory |
| Binary | `stow` built with `-trimpath -ldflags "-s -w"` |

Peak RSS is sampled from the server child process while it holds the payload,
not from the benchmark process.

## Lifecycle, 30 sequential sessions, 1 MiB payload

| Metric | min | p50 | p95 | max |
|---|---:|---:|---:|---:|
| Time to ready (spawn to endpoint) | 14.97 ms | 15.35 ms | 15.99 ms | 16.92 ms |
| First operation (create + put + get) | 19.79 ms | 23.09 ms | 61.45 ms | 65.86 ms |
| Total to first operation | 37.44 ms | 41.07 ms | 89.17 ms | 92.19 ms |
| Shutdown | 33.97 ms | 35.50 ms | 49.14 ms | 51.10 ms |
| Peak child RSS | 16.7 MB | 17.7 MB | 21.2 MB | 22.4 MB |

Time to ready is flat across payload sizes (14.7 ms empty, 17.0 ms at 8 MiB), so
startup cost is process and protocol cost, not data cost.

## Memory scaling

Eight sessions per point.

| Payload | Peak RSS p50 | Time to ready p50 |
|---:|---:|---:|
| 0 B | 11.6 MB | 14.68 ms |
| 1 MiB | 17.3 MB | 15.10 ms |
| 2 MiB | 22.6 MB | 15.59 ms |
| 4 MiB | 29.9 MB | 15.57 ms |
| 8 MiB | 48.3 MB | 16.96 ms |

**Derived: 11.62 MB fixed process cost, and 4.59 MB of peak RSS per MiB of
object data.**

## What this changes

**1. The plan's memory-overshoot target is wrong by more than an order of
magnitude.** Section 11 asks for "peak memory overshoot over configured limit
< 20%". The measured multiplier is 4.59x, because the v1 write path buffers the
whole request body for authentication and checksum validation, and the runtime
copies it again before storing. A quota is not a memory bound today. This makes
the streaming or single-copy write path in phase 2 a scale requirement, not a
polish item, and the target should be restated as a reduction in the multiplier
rather than a percentage of the limit.

### 1a. Corrected: the multiplier is GC headroom, not the number of copies

The explanation above attributed the multiplier to the number of full-body
copies. That was tested and it is wrong. The write path made four or five
independent full-size copies of every request body: SigV4 payload preparation,
the Content-Length comparison, Content-MD5, the checksum algorithm, and the
runtime's own read-then-copy before storing.

Collapsing the four copies inside `internal/s3api` into one, by reading the body
once per request and sharing those bytes with every check, changed nothing
measurable. Three runs of the sweep on each build, same machine, same session
defaults:

| Build | multiplier samples | median |
|---|---:|---:|
| before (four copies in `s3api`) | 4.56, 4.48, 4.68 | 4.56 |
| after (one copy in `s3api`) | 4.52, 4.49, 4.48 | 4.49 |

The distributions overlap. A change that removed three quarters of the copies in
one layer moved the number by less than the run-to-run spread.

Varying the Go GC instead, with no code change at all:

| `GOGC` | multiplier | 4 MiB put p50 |
|---|---:|---:|
| 100 (default) | 4.62 | 111.7 ms |
| 50 | 3.34 | 112.3 ms |
| 20 | 3.07 | 113.1 ms |

`GOMEMLIMIT=64MiB` alone changed nothing (4.49), which is consistent with the
heap never approaching the limit.

**The multiplier is dominated by GC headroom, not by copy count.** Peak RSS
tracks the largest simultaneously live set, times the GC target. The copies
removed above are transient: each became garbage as soon as the next stage
consumed it. The live set is the body plus the runtime's copy, and the default
`GOGC=100` then allows the heap to roughly double on top of that.

Two consequences, both of which redirect phase 2:

- Removing redundant copies is worth doing for clarity and for the CPU spent
  hashing the body more than once, but it will not move peak RSS. Expect no
  memory number from it, and do not re-run this experiment expecting one.

### 1b. Removing the two redundant copies on the runtime side does help

The copies that are *simultaneously live* are what peak RSS tracks. Two of them
were redundant: the store adapter read the body into a second buffer, and the
runtime then copied it a third time before handing it to a store that copies it
again anyway, through `ETagForReader`.

Both stores in this package make their own copy of the body, so the runtime's
copy was doubly redundant and could not introduce aliasing. The adapter now
takes the bytes from a reader that already holds them, and the runtime hands the
caller's buffer to the store as it is.

Three sweep runs per build, same machine, same session defaults:

| Build | multiplier samples | median | 4 MiB put p50 |
|---|---:|---:|---:|
| before | 4.61, 4.59, 4.53 | 4.59 | 85.5 ms |
| after | 4.23, 4.22, 4.20 | 4.22 | 84.7 ms |

The distributions do not overlap, and put latency is unchanged, so unlike the
s3api change this one is free.

**A floor remains at 2x live**, which is why 4.22 rather than the 2.3 the
arithmetic suggested. The request buffer and the store's own retained copy are
live at the same moment, and only the store's is unavoidable. Removing
intermediates shaves the GC headroom on top of a live set it cannot shrink.
Bringing the live set below 2x means the store adopting a caller's buffer, which
is a contract change to `storage.Store` rather than a local optimisation.

**The safety of removing the runtime's copy was incidental and is now
enforced.** It holds only while the runtime cannot hand the store a reader that
exposes the caller's bytes, which today is true solely because `bytes.Reader` has
no `Bytes` method. `TestRuntimeNeverExposesCallerBytesToTheStore` pins that, so a
later optimisation that exposes the bytes fails there instead of letting a store
silently adopt a buffer the caller may reuse.

**Correction to the GC figures in 1a.** The 4 MiB put latency was 111.7 ms when
they were taken, so GOGC=20 looked like it cost about 1%. With the two redundant
copies gone the baseline is 85 ms and the same setting costs about 20%
(100.7 ms). The copy work was masking the GC cost. GC tuning is a real trade, not
a free win, and the two levers are independent rather than alternatives:

| Build | GOGC=100 | GOGC=50 | GOGC=20 |
|---|---:|---:|---:|
| with the copy fix | 4.19 | 3.27 | 2.96 |

**Sessions now set `GOGC=50` on their own server.** This is the one lever that
works, and it is now applied rather than merely recommended. A session is an
ephemeral local fixture and one of several on the machine, so its peak memory is
what matters and its throughput rarely is; a long-lived `stow serve` is the
opposite and is deliberately left alone. The setting goes on the session's child
process only, so it cannot affect any other server, and a `GOGC` the caller set
themselves wins over the default. Both clients use the same value and
`check-version.mjs` fails if they drift.

50 rather than 20 because it takes most of the benefit for a fraction of the
cost: 4.19 to 3.27 against 4.19 to 2.96. The put-latency figures for these were
taken on a busy machine and are not reliable enough to publish as a number; the
multiplier is a memory measurement and was stable across every run.

Streaming the write path would remove the live set itself rather than shaving
headroom, and remains the only route to a multiplier near 1.

### 1c. Letting the store adopt a caller's buffer made memory worse

The 2x live floor in 1b can only be broken by letting the store take the
caller's bytes as the stored object instead of copying them. That was built and
measured: an unexported `putObjectOwned` on the engine, an optional capability a
store satisfies structurally, and `MemoryStore.PutObjectOwned` retaining the
caller's slice. Nothing new was exported, and a store without the capability
still got a copy, so it was safe to try.

It lost. Three sweep runs per build, the benchmark spawning the legacy server
path so the collector target is the Go default in both builds:

| GOGC | copying (`9316e4c`) | adopting | delta |
|---|---:|---:|---:|
| 100 | 4.48, 4.24, 4.18 | 4.66, 4.65, 4.64 | worse by 0.41 |
| 50 | 3.26 | 3.37 | worse by 0.11 |
| 20 | 2.90 | 3.04 | worse by 0.14 |

The distributions do not overlap, and the sign is the same at every collector
target. The change was reverted.

**The copy was the thing keeping the collector honest.** At `GOGC=100` the heap
is allowed to grow to roughly twice the live set before a collection, so what
bounds peak RSS is how often the collector runs, not how much is live. The
redundant full-size copy was allocation pressure that triggered extra
collections, holding the heap below its ceiling. Remove the copy and the
collector runs less often, so the heap grows further before each cycle and peak
RSS rises. The shrinking penalty at lower `GOGC` fits: once the collector is
already running frequently, there is less headroom for a removed copy to
recover.

This is the third measurement to contradict the plan's copy-count account, and
the first from the opposite direction. Two earlier experiments found that
removing copies did not help; this one found that removing one *hurt*.

**The practical consequence is that the plan's phase-2 target cannot be reached by
removing copies**, in either direction. Lowering the multiplier means either a
lower collector target, which is a direct and measured trade, or not holding the
body resident at all, which is the streaming question. Counting copies is not a
lever. Jev put the value of the contract change at 1.38 of 4 before it was
built, and it turned out to be negative; that is worth remembering next time a
memory model has an obvious-looking fix attached to it.

**2. The 64 MiB / 10,000 object default is wrong for an ephemeral agent
session.** At the measured multiplier, a 64 MiB session budget implies roughly
300 MB of peak RSS per session. One hundred parallel sessions would need
around 30 GB, which the plan's "100 parallel default sessions all acquire and
release successfully" target cannot assume on an ordinary machine.

Recommended default for a scoped session, from this data:

- `maxBytes` = **16 MiB**, implying about 85 MB peak RSS per session;
- `maxObjects` = **1,000**, which is far above a typical agent workload of a few
  input and output files and costs almost nothing;
- per-request body cap stays at **8 MiB**, which already bounds a single
  PutObject.

That supports realistic task payloads while keeping a 100-session fan-out in the
single-digit gigabytes, which is a number to state and test rather than assume.

**3. Latency targets in section 11 are met with headroom and can be adopted.**
Time to ready p50 of 15.35 ms is well inside the proposed 50 ms, and total to
first operation p50 of 41 ms is well inside the proposed 500 ms. Shutdown p50 of
35.5 ms is well inside the proposed 1 s. Adopt them, and re-measure on the CI
runner before treating them as gates.

**4. The p95 tail on the first operation is wide.** 23 ms p50 against 61 ms
p95, on an otherwise idle machine, is most likely Go garbage collection and
first-use SigV4 credential setup. It is worth understanding before the p95
becomes a gate, but it is not large enough to block the release.

## Not measured

- Parallel sessions. The plan's 100-parallel target is unmeasured and should not
  be claimed until it is.
- Filesystem backend. Only the memory backend was measured.
- Cold versus warm binary cache.
- Any platform other than linux x64. A macOS arm64 run is required before these
  numbers are treated as the reference for the first release.
