# Session performance baseline

**Status:** recorded
**Date:** 2026-09-25
**Purpose:** give the targets in `docs/agent-dx-plan.md` section 11 a measured
starting point, and size the default session quotas from data.

Reproduce with:

```sh
make build
node packages/stow/scripts/benchmark-session.mjs --sessions 30 --payload-bytes 1048576
node packages/stow/scripts/benchmark-session.mjs --sweep
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
