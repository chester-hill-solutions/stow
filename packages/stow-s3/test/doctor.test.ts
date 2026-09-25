import assert from "node:assert/strict";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { describe, it } from "node:test";
import {
  renderDoctorReport,
  runDoctor,
  type DoctorCheck,
  type DoctorReport,
} from "../dist/doctor.js";
import { resolveStowBinaryDetailed, stowBinaryAvailable } from "../dist/bin.js";
import { READY_PROTOCOL_VERSION } from "../dist/ready.js";

const hasBinary = stowBinaryAvailable();

function checkNamed(report: DoctorReport, name: string): DoctorCheck | undefined {
  return report.checks.find((check) => check.name === name);
}

describe("stow doctor", () => {
  it("reports the client facts the binary cannot observe", async () => {
    const report = await runDoctor();
    const resolution = checkNamed(report, "client.binary.resolution");
    assert.ok(resolution, "the report must say how the binary was resolved");
    assert.equal(resolution.ok, true);
    assert.match(resolution.detail ?? "", /from /);

    // The source is the fact that distinguishes "not installed" from "installed
    // but bypassed by STOW_BIN", which a bare path cannot express.
    const resolved = resolveStowBinaryDetailed();
    assert.ok(
      ["platform-package", "environment", "monorepo", "path"].includes(resolved.source),
      `unexpected resolution source ${resolved.source}`,
    );

    const runtime = checkNamed(report, "client.runtime");
    assert.ok(runtime, "the report must include the client runtime");
    assert.equal(runtime.ok, true);
  });

  it("never marks the optional upstream check required", async () => {
    const report = await runDoctor();
    const upstream = checkNamed(report, "s3.upstream");
    if (upstream !== undefined) {
      assert.equal(
        upstream.required,
        false,
        "a local-only user has no upstream, which must not fail the report",
      );
    }
  });

  it("merges the binary's own checks with the client's", { skip: !hasBinary }, async () => {
    const report = await runDoctor();
    for (const name of [
      "binary.version",
      "platform",
      "tempDir",
      "backend",
      "endpoint.health",
    ]) {
      assert.ok(checkNamed(report, name), `the binary check ${name} must be merged in`);
    }
    assert.equal(report.protocolVersion, READY_PROTOCOL_VERSION);
    assert.match(report.binaryVersion, /^\d+\.\d+\.\d+$/);
  });

  it("succeeds on a machine that can run a session", { skip: !hasBinary }, async () => {
    const report = await runDoctor();
    assert.equal(report.ok, true, renderDoctorReport(report));
  });

  it("fails with an actionable message when no binary can be found", async () => {
    const previous = process.env.STOW_BIN;
    process.env.STOW_BIN = "/nonexistent/stow";
    try {
      const report = await runDoctor({ timeoutMs: 5_000 });
      assert.equal(report.ok, false);
      const resolution = checkNamed(report, "client.binary.resolution");
      assert.equal(resolution?.ok, false);
      assert.equal(resolution?.required, true);
      assert.match(resolution?.error ?? "", /no runnable stow binary/);

      // A missing binary is a finding in the report, not a thrown error, so the
      // other checks still reach the person debugging it.
      const binaryDoctor = checkNamed(report, "client.binary.doctor");
      assert.equal(binaryDoctor?.ok, false);
      assert.equal(binaryDoctor?.required, true);
    } finally {
      if (previous === undefined) {
        delete process.env.STOW_BIN;
      } else {
        process.env.STOW_BIN = previous;
      }
    }
  });

  it("reports a binary that does not speak the protocol instead of throwing", async () => {
    const dir = await mkdtemp(join(tmpdir(), "stow-doctor-fake-"));
    const fake = join(dir, "stow");
    await writeFile(fake, "#!/bin/sh\nexit 0\n", { mode: 0o755 });
    const previous = process.env.STOW_BIN;
    process.env.STOW_BIN = fake;
    try {
      const report = await runDoctor({ timeoutMs: 5_000 });
      assert.equal(report.ok, false);
      const binaryDoctor = checkNamed(report, "client.binary.doctor");
      assert.match(binaryDoctor?.error ?? "", /did not return JSON/);
    } finally {
      if (previous === undefined) {
        delete process.env.STOW_BIN;
      } else {
        process.env.STOW_BIN = previous;
      }
      await rm(dir, { recursive: true, force: true });
    }
  });

  it("renders required failures as failures and optional ones as warnings", () => {
    const report: DoctorReport = {
      protocolVersion: 1,
      binaryVersion: "0.0.0",
      ok: false,
      checks: [
        { name: "ok", ok: true, required: true, detail: "fine" },
        { name: "broken", ok: false, required: true, error: "could not bind" },
        { name: "optional", ok: false, required: false, error: "not configured" },
      ],
    };
    const rendered = renderDoctorReport(report);
    assert.match(rendered, /FAIL\s+broken\s+could not bind/);
    assert.match(rendered, /warn\s+optional\s+not configured/);
    assert.match(rendered, /1 required check\(s\) failed\./);
  });

  it("keeps a multi-line error from breaking the report layout", () => {
    const report: DoctorReport = {
      protocolVersion: 1,
      binaryVersion: "0.0.0",
      ok: false,
      checks: [
        {
          name: "binary.doctor",
          ok: false,
          required: true,
          error: "usage: stow <command>\n\ncommands:\n  serve  start",
        },
      ],
    };
    const rendered = renderDoctorReport(report);
    for (const line of rendered.split("\n")) {
      assert.ok(
        line.length < 200,
        `a rendered line should stay on one line, got ${JSON.stringify(line)}`,
      );
    }
    assert.match(rendered, /usage: stow <command> commands:/);
  });
});
