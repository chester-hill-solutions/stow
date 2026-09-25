---
status: accepted
---

# Embedded runtime and capability profiles

This ADR records the portable runtime direction for Stow after the 0.2 remediation foundation. It is additive: the existing process-owned `Stow.start()` and endpoint-owned `Stow.connect()` contracts remain unchanged.

## Context

Stow began as an S3-compatible HTTP service. That interface is useful for AWS SDK compatibility, but it is not the smallest or most portable interface for an application that already runs in the same process as its object store. A direct runtime must also make its persistence, upstream, and durability capabilities explicit instead of silently inheriting HTTP or environment behavior.

## Decisions

### 1. Direct runtime is the primary embedded interface

The first embedded profile is a direct in-process API with explicit `open`, bucket and object operations, quotas, usage and capability reporting, `reset`, and `close`. The public Go entry point is `pkg/stow`; callers do not depend on `internal/storage` or `internal/runthrough`.

The first profile is memory-only. It has no filesystem, network, signal, environment, Node, or AWS SDK dependency. Each instance owns its own data and is isolated from every other instance.

### 2. S3 HTTP remains an adapter

The native S3 server remains a compatibility adapter over the storage and run-through behavior. It is not the source of truth for the embedded interface, and the embedded profile does not create an HTTP listener. Existing SigV4, XML, CORS, admin routes, CLI flags, and SDK behavior remain governed by the S3 compatibility contract.

### 3. Capabilities are explicit

A profile reports persistence, multipart, upstream propagation, and quota capabilities. Unsupported combinations fail with a structured error; they do not silently fall back to a native filesystem, remote service, or ephemeral upstream mirror.

Mirror writes require a durable outbox. A memory-only profile without such an outbox rejects live upstream mutations rather than pretending that propagation is durable.

### 4. WASM is a host adapter, not a second object model

The `js/wasm` artifact uses the same platform-neutral memory runtime. Its first host bridge is a small JSON/base64 protocol so browser and Node hosts can provide their own byte and error marshalling. The bridge is capability-gated and additive; it does not change the TypeScript process or endpoint entry points.

### 5. Compatibility remains testable

Direct-runtime tests exercise lifecycle, isolation, quotas, reset, and close. The WASM test exercises the host bridge against the same memory behavior. Existing S3 conformance continues to run separately against memory and filesystem stores so an adapter change cannot silently redefine direct-runtime semantics.

## Consequences

- The public Go package is intentionally small and backend-neutral; internal storage details may evolve without becoming API.
- The first WASM host protocol is explicit about serialization and must be versioned when its public shape changes.
- A later filesystem or upstream profile must satisfy the same lifecycle, quota, error, and isolation contract before it is advertised.
- The native server and the embedded runtime are tested as separate seams. A future HTTP-over-runtime refactor must preserve both S3 conformance and direct-runtime tests.

## Out of scope

This ADR does not add browser persistence, automatic bucket provisioning, a public S3 server artifact, a new cache abstraction, or a replacement for the existing TypeScript lifecycle APIs.
