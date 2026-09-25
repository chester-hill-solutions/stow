/**
 * One reported fact, in the same shape the `stow doctor` subcommand emits.
 *
 * The shape is a flat list rather than a fixed object per subsystem so that this
 * client can append checks the binary cannot observe, such as how the binary was
 * resolved, and the two merge without either side knowing the other's fields.
 */
export interface DoctorCheck {
    readonly name: string;
    readonly ok: boolean;
    /**
     * Required separates "this is broken" from "this capability is absent and you
     * may not need it". A local-only user has no upstream S3 configured, which is
     * a normal state, so a report that failed on it would be noise.
     */
    readonly required: boolean;
    readonly detail?: string;
    readonly error?: string;
}
export interface DoctorReport {
    readonly protocolVersion: number;
    readonly binaryVersion: string;
    readonly ok: boolean;
    readonly checks: readonly DoctorCheck[];
}
export interface DoctorOptions {
    /** Milliseconds to wait for the binary's own report. */
    readonly timeoutMs?: number;
}
/**
 * Collect a full diagnostic: what this client can see, then what the binary can
 * see, merged into one report.
 */
export declare function runDoctor(options?: DoctorOptions): Promise<DoctorReport>;
/** Render a report for a person, in the same layout the binary uses. */
export declare function renderDoctorReport(report: DoctorReport): string;
/**
 * Entry point for the stow-doctor command. Exits non-zero when a required check
 * failed so a CI job notices, and with a distinct status when the diagnostic
 * could not run at all, so the two are tellable apart.
 */
export declare function runDoctorCli(args: string[]): Promise<number>;
//# sourceMappingURL=doctor.d.ts.map