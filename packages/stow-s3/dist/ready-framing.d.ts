/**
 * Framing for the versioned readiness protocol.
 *
 * The protocol writes exactly one JSON object per line to the readiness
 * descriptor. A pipe delivers that line in arbitrarily sized pieces, so a reader
 * must accumulate until it sees the newline that terminates the record.
 *
 * This is separate from the reader only so it can be tested directly. The
 * failure it prevents is subtle: parsing the trailing unterminated fragment
 * hands a partial record to the JSON parser, and the syntax error that follows
 * is thrown from a stream callback where it rejects nothing, leaving startup to
 * hang until its timeout.
 */
export interface FramedRecords {
    /** Complete, newline-terminated records, with the terminators removed. */
    records: string[];
    /** The unterminated tail, which must be carried into the next read. */
    rest: string;
}
/**
 * Split an accumulated buffer into the records it has completed and the
 * remainder it still needs.
 *
 * A record is only complete once a newline follows it. The final element of the
 * split is either an empty string, when the buffer ended exactly on a newline,
 * or a fragment that is carried forward.
 */
export declare function takeCompleteRecords(buffer: string): FramedRecords;
//# sourceMappingURL=ready-framing.d.ts.map