// The ratchet decision, in one place.
//
// Four gates enforce the same policy and each had written it out by hand: the
// TypeScript lint and DRY gates compared per-key counts, the file-size and
// type-escape gates compared identity sets, and all four grew a third check —
// whether the baseline itself had grown — separately. That is the failure this
// repository names in its own architecture document: a decision with one correct
// answer, written down more than once by hand, where every copy is small and
// every copy is individually plausible.
//
// It also meant the policy had no test. A gate that has quietly stopped rejecting
// new debt is worse than no gate, because the debt is still described as
// baselined. These functions are pure so the policy can be asserted directly
// rather than inferred from four scripts that happen to exit non-zero.

// compareKeys reports, for each key, whether the current value is above the
// baseline (new debt), below it (an improvement the baseline has not recorded),
// or equal.
//
// A reduction is a failure as well as an increase, and deliberately so: the
// baseline is a floor, and a floor that stays put after the floor is met is a
// floor that stops describing anything. Lowering it is the only way the next
// regression is caught.
export function compareKeys(current, baseline, keys) {
  const regressions = [];
  const stale = [];
  for (const key of keys) {
    const actual = current[key] ?? 0;
    const allowed = baseline[key] ?? 0;
    if (actual > allowed) regressions.push(`${key}: ${actual} > ${allowed}`);
    if (actual < allowed) stale.push(`${key}: ${actual} < ${allowed}; lower the baseline`);
  }
  return { regressions, stale };
}

// expandedKeys reports the keys where the baseline claims more than the previous
// baseline did. This is what makes a baseline impossible to grow quietly: the
// per-key comparison above is satisfied by raising a limit and lowering it again,
// and only a comparison against history catches that.
//
// A key the previous baseline did not record counts as zero, so introducing a new
// metric is a visible event rather than a free addition.
export function expandedKeys(baseline, previous, keys) {
  if (!previous) return [];
  return keys.filter((key) => (baseline[key] ?? 0) > (previous[key] ?? 0));
}

// compareIdentities reports identities present now but not in the baseline, and
// baseline entries no longer present. Both are failures, and for the same two
// reasons as compareKeys: one is unapproved debt, the other is debt that was paid
// and not recorded.
//
// Set membership rather than a count, so renaming a violation does not launder
// it: a renamed entry is one addition and one removal, and the count is
// unchanged, which is why compareKeys on a count would miss it entirely.
export function compareIdentities(current, baseline) {
  const allowed = new Set(baseline);
  // Deduplicated: a scan that reports the same identity twice must not turn one
  // violation into two lines of report, and a caller comparing lengths would
  // otherwise read a repeated entry as growth.
  const present = [...new Set(current)];
  const seen = new Set(present);
  return {
    added: present.filter((item) => !allowed.has(item)),
    stale: [...allowed].filter((item) => !seen.has(item)),
  };
}
