import { execFile } from "node:child_process";
import { createRequire } from "node:module";
import { platformPackageForCurrentPlatform, resolveStowBinaryDetailed, stowBinaryAvailable, } from "./bin.js";
import { READY_PROTOCOL_VERSION } from "./ready.js";
const requireFromHere = createRequire(import.meta.url);
const packageVersion = requireFromHere("../package.json").version;
const DOCTOR_TIMEOUT_MS = 20_000;
const MAX_DIAGNOSTIC_BYTES = 64 * 1024;
const SOURCE_DESCRIPTIONS = {
    "platform-package": "the platform package installed alongside this package",
    environment: "the STOW_BIN environment variable",
    monorepo: "bin/stow-s3 in the monorepo checkout",
    path: "stow on PATH",
};
function succeeded(name, required, detail) {
    return { name, ok: true, required, detail };
}
function failed(name, required, error, detail) {
    return detail === undefined
        ? { name, ok: false, required, error }
        : { name, ok: false, required, error, detail };
}
// Client-side checks. None of these are observable from inside the binary: it
// cannot see which npm platform package was installed, what STOW_BIN said, or
// whether the JavaScript and the binary are the same release.
function clientChecks() {
    const resolved = resolveStowBinaryDetailed();
    const available = stowBinaryAvailable();
    const resolution = available
        ? succeeded("client.binary.resolution", true, `${resolved.path} (from ${SOURCE_DESCRIPTIONS[resolved.source]})`)
        : failed("client.binary.resolution", true, "no runnable stow binary was found", resolved.path);
    const expectedPackage = platformPackageForCurrentPlatform();
    const platformCheck = expectedPackage === undefined
        ? failed("client.platform.package", false, `no stow binary is published for ${process.platform}-${process.arch}`)
        : succeeded("client.platform.package", false, resolved.source === "platform-package"
            ? `${expectedPackage} is installed`
            : `${expectedPackage} was not used; resolved from ${SOURCE_DESCRIPTIONS[resolved.source]}`);
    return [
        succeeded("client.runtime", false, `node ${process.version} on ${process.platform}-${process.arch}`),
        resolution,
        platformCheck,
    ];
}
/**
 * Run the binary's own diagnostic and return its parsed report.
 *
 * A failure here is returned rather than thrown, because "the binary would not
 * answer" is itself the finding a user needs, and it has to appear in the
 * report instead of replacing it.
 */
function runBinaryDoctor(binary, timeoutMs) {
    return new Promise((resolve) => {
        execFile(binary, ["doctor", "--json"], { timeout: timeoutMs, maxBuffer: MAX_DIAGNOSTIC_BYTES, encoding: "utf8" }, (error, stdout, stderr) => {
            if (error !== null) {
                const detail = stderr.trim() || error.message;
                resolve({ error: `could not run "${binary} doctor --json": ${detail}` });
                return;
            }
            try {
                resolve({ report: JSON.parse(stdout) });
            }
            catch (parseError) {
                resolve({
                    error: `"${binary} doctor --json" did not return JSON: ${parseError instanceof Error ? parseError.message : String(parseError)}`,
                });
            }
        });
    });
}
function versionCheck(binaryVersion) {
    if (binaryVersion === undefined) {
        return failed("client.version.match", false, "the binary did not report a version");
    }
    if (binaryVersion === packageVersion) {
        return succeeded("client.version.match", false, `package and binary are both ${packageVersion}`);
    }
    // Not required: pointing STOW_BIN at a different build is a deliberate thing
    // to do, and the report should say so rather than forbid it.
    return failed("client.version.match", false, `this package is ${packageVersion} but the binary is ${binaryVersion}`);
}
/**
 * Collect a full diagnostic: what this client can see, then what the binary can
 * see, merged into one report.
 */
export async function runDoctor(options = {}) {
    const timeoutMs = options.timeoutMs ?? DOCTOR_TIMEOUT_MS;
    const resolved = resolveStowBinaryDetailed();
    const checks = clientChecks();
    let binaryVersion;
    if (stowBinaryAvailable()) {
        const { report, error } = await runBinaryDoctor(resolved.path, timeoutMs);
        if (report !== undefined) {
            checks.push(...report.checks);
            binaryVersion = report.binaryVersion;
        }
        else {
            checks.push(failed("client.binary.doctor", true, error ?? "the binary diagnostic failed"));
        }
    }
    else {
        checks.push(failed("client.binary.doctor", true, "skipped because no runnable stow binary was found"));
    }
    checks.push(versionCheck(binaryVersion));
    return {
        protocolVersion: READY_PROTOCOL_VERSION,
        binaryVersion: binaryVersion ?? "unknown",
        ok: checks.every((check) => !check.required || check.ok),
        checks,
    };
}
/**
 * Collapse whitespace so a multi-line error, such as a usage message captured
 * from the binary, cannot break the alignment of the report.
 */
function oneLine(value) {
    return value.replace(/\s+/g, " ").trim();
}
/** Render a report for a person, in the same layout the binary uses. */
export function renderDoctorReport(report) {
    const width = report.checks.reduce((widest, check) => Math.max(widest, check.name.length), 0);
    const lines = [
        `stow doctor (protocol ${report.protocolVersion}, binary ${report.binaryVersion})`,
        "",
    ];
    let failures = 0;
    for (const check of report.checks) {
        let status = "ok  ";
        if (!check.ok) {
            if (check.required) {
                status = "FAIL";
                failures += 1;
            }
            else {
                status = "warn";
            }
        }
        const detail = check.error === undefined
            ? (check.detail ?? "")
            : check.detail === undefined
                ? check.error
                : `${check.detail}: ${check.error}`;
        lines.push(`  ${status}  ${check.name.padEnd(width)}  ${oneLine(detail)}`.trimEnd());
    }
    lines.push("");
    lines.push(failures === 0
        ? "No required checks failed."
        : `${failures} required check(s) failed.`);
    return `${lines.join("\n")}\n`;
}
/**
 * Entry point for the stow-doctor command. Exits non-zero when a required check
 * failed so a CI job notices, and with a distinct status when the diagnostic
 * could not run at all, so the two are tellable apart.
 */
export async function runDoctorCli(args) {
    const asJson = args.includes("--json");
    const report = await runDoctor();
    if (asJson) {
        process.stdout.write(`${JSON.stringify(report)}\n`);
    }
    else {
        process.stdout.write(renderDoctorReport(report));
    }
    return report.ok ? 0 : 1;
}
//# sourceMappingURL=doctor.js.map