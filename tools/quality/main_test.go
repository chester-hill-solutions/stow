package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The ratchet is the gate every other gate is trusted because of, and it had no
// test of its own: the comparison lived inside main, so there was nothing to
// call and nothing to assert. The three times it fired during one afternoon —
// complexity 4 > 3, max-params 10 > 8, and a new oversized file — were all
// caught by the code, and none of them by a test that would have noticed if the
// code had stopped working.
//
// The four behaviours below are the ones the gate exists to provide. Each is a
// way it can be wrong in the direction that matters: a gate that has silently
// stopped rejecting new debt is worse than no gate, because the debt is still
// being described as baselined.

func reportOf(counts map[string]int, identities ...string) report {
	out := report{Version: baselineVersion, Counts: counts}
	for _, identity := range identities {
		out.Violations = append(out.Violations, violation{Rule: "complexity", Identity: identity, Message: "recorded"})
	}
	return out
}

func TestRatchetAcceptsAnUnchangedTree(t *testing.T) {
	baseline := reportOf(map[string]int{"complexity": 2}, "a.go:one#1", "a.go:two#1")
	if problems := ratchetProblems(baseline, baseline); len(problems) != 0 {
		t.Fatalf("an unchanged tree reported %v", problems)
	}
	if problems := countIncreases(baseline, baseline); len(problems) != 0 {
		t.Fatalf("an unchanged tree reported an increase: %v", problems)
	}
}

// A new identity is unapproved debt. This is the check that fired twice during
// the session that produced this file.
func TestRatchetRejectsANewIdentity(t *testing.T) {
	baseline := reportOf(map[string]int{"complexity": 1}, "a.go:one#1")
	current := reportOf(map[string]int{"complexity": 2}, "a.go:one#1", "a.go:newcomer#1")

	problems := ratchetProblems(current, baseline)
	if len(problems) != 1 {
		t.Fatalf("problems = %v, want exactly the new identity", problems)
	}
	if !strings.Contains(problems[0], "a.go:newcomer#1") || !strings.HasPrefix(problems[0], "new: ") {
		t.Errorf("problem = %q, want it to name the new identity", problems[0])
	}
}

// A stale identity is debt that was paid and not recorded. It fails so the
// baseline has to be lowered, which is the only mechanism by which the ratchet
// ever gets stricter.
func TestRatchetRejectsAStaleIdentity(t *testing.T) {
	baseline := reportOf(map[string]int{"complexity": 2}, "a.go:one#1", "a.go:fixed#1")
	current := reportOf(map[string]int{"complexity": 1}, "a.go:one#1")

	problems := ratchetProblems(current, baseline)
	if len(problems) != 1 {
		t.Fatalf("problems = %v, want exactly the stale identity", problems)
	}
	if !strings.Contains(problems[0], "a.go:fixed#1") || !strings.HasPrefix(problems[0], "stale: ") {
		t.Errorf("problem = %q, want it to name the stale identity", problems[0])
	}
}

// Both at once is the ordinary case when unrelated work lands together, and the
// report has to carry both rather than stopping at the first.
func TestRatchetReportsNewAndStaleTogether(t *testing.T) {
	baseline := reportOf(map[string]int{"complexity": 2}, "a.go:kept#1", "a.go:paid#1")
	current := reportOf(map[string]int{"complexity": 2}, "a.go:kept#1", "a.go:added#1")

	problems := ratchetProblems(current, baseline)
	if len(problems) != 2 {
		t.Fatalf("problems = %v, want the new and the stale identity", problems)
	}
}

// The count check is what stops a violation being renamed to shed its baseline
// entry. Identity matching alone would see one new and one stale and an
// unchanged total; only the count notices that the rule did not get better.
func TestCountCheckCatchesARenamedViolation(t *testing.T) {
	previous := reportOf(map[string]int{"complexity": 1}, "a.go:before#1")
	// Same total, different identity: the violation was renamed rather than fixed.
	current := reportOf(map[string]int{"complexity": 1}, "a.go:after#1")

	if problems := countIncreases(current, previous); len(problems) != 0 {
		t.Fatalf("a rename is not a count increase, got %v", problems)
	}
	// The ratchet still rejects it, on identity grounds, which is the point.
	if problems := ratchetProblems(current, previous); len(problems) == 0 {
		t.Error("a renamed violation was accepted")
	}
}

func TestCountCheckRejectsAnIncrease(t *testing.T) {
	previous := reportOf(map[string]int{"complexity": 1, "max-params": 2}, "a.go:one#1")
	current := reportOf(map[string]int{"complexity": 1, "max-params": 3}, "a.go:one#1")

	problems := countIncreases(current, previous)
	if len(problems) != 1 {
		t.Fatalf("problems = %v, want the max-params increase", problems)
	}
	if !strings.Contains(problems[0], "max-params") || !strings.Contains(problems[0], "3 > 2") {
		t.Errorf("problem = %q, want it to name the rule and both counts", problems[0])
	}
}

// A rule previous never recorded counts as an increase from zero, so introducing
// a new metric is a visible event rather than a free addition.
func TestCountCheckTreatsAnUnknownRuleAsAnIncrease(t *testing.T) {
	previous := reportOf(map[string]int{"complexity": 1})
	current := reportOf(map[string]int{"complexity": 1, "any": 1})

	problems := countIncreases(current, previous)
	if len(problems) != 1 || !strings.Contains(problems[0], "any (1 > 0)") {
		t.Fatalf("problems = %v, want a new rule counted from zero", problems)
	}
}

// An improvement is not an increase. A rule that went down must pass, or fixing
// debt would fail the gate.
func TestCountCheckAcceptsAnImprovement(t *testing.T) {
	previous := reportOf(map[string]int{"complexity": 3, "any": 2})
	current := reportOf(map[string]int{"complexity": 1})

	if problems := countIncreases(current, previous); len(problems) != 0 {
		t.Fatalf("a reduction reported %v", problems)
	}
}

// Order is not a decision. Two violations of the same rule must not make the
// report depend on map iteration order, or the same tree fails differently on
// different runs.
func TestCountCheckIsDeterministic(t *testing.T) {
	previous := reportOf(map[string]int{"a": 0, "b": 0, "c": 0, "d": 0, "e": 0})
	current := reportOf(map[string]int{"a": 1, "b": 1, "c": 1, "d": 1, "e": 1})

	first := strings.Join(countIncreases(current, previous), "\n")
	for i := 0; i < 20; i++ {
		if got := strings.Join(countIncreases(current, previous), "\n"); got != first {
			t.Fatalf("report order varies between runs:\n%s\n---\n%s", first, got)
		}
	}
}

// The scan has to see the same code the gate runs over. A rule that never fires
// is indistinguishable from a rule that has been satisfied, so each metric is
// provoked on a real file and asserted to be reported.
func TestScanReportsEachMetricItClaimsToCheck(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "internal", "sample")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	source := `package sample

import "any"

// wide takes more than five parameters, so max-params must fire.
func wide(a, b, c, d, e, f int) any { return nil }

// deep is over the complexity ceiling of 15.
func deep(n int) int {
	total := 0
	for i := 0; i < n; i++ {
		if i%2 == 0 {
			total++
		} else if i%3 == 0 {
			total--
		} else if i%5 == 0 {
			total += 2
		} else if i%7 == 0 {
			total -= 2
		} else if i%11 == 0 {
			total++
		} else if i%13 == 0 {
			total--
		} else if i%17 == 0 {
			total += 3
		} else if i%19 == 0 {
			total -= 3
		} else if i%23 == 0 {
			total++
		} else if i%29 == 0 {
			total--
		} else if i%31 == 0 {
			total += 4
		} else if i%37 == 0 {
			total -= 4
		} else if i%41 == 0 {
			total++
		} else if i%43 == 0 {
			total--
		} else if i%47 == 0 {
			total += 5
		} else if i%53 == 0 {
			total -= 5
		}
	}
	return total
}
`
	if err := os.WriteFile(filepath.Join(root, "sample.go"), []byte(source), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// scan walks fixed roots, so it is exercised against a temporary checkout
	// rather than the repository: the alternative is a fixture inside the tree,
	// which would itself be scanned and would need its own baseline entry.
	result, err := scanRoots([]string{filepath.Join(dir, "internal")})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	found := map[string]bool{}
	for _, item := range result.Violations {
		found[item.Rule] = true
	}
	for _, rule := range []string{"max-params", "complexity", "any"} {
		if !found[rule] {
			t.Errorf("scan did not report %s; violations were %+v", rule, result.Violations)
		}
	}
	if len(result.Violations) == 0 {
		t.Fatal("scan reported nothing at all for a file that breaks three rules")
	}
}

// A file over the line ceiling must be named, since file-size is the one metric
// whose failure is a whole file rather than a location inside it.
func TestScanReportsFunctionLengthOverTheCeiling(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "internal", "long")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	var body strings.Builder
	body.WriteString("package long\n\nfunc enormous() {\n")
	for i := 0; i < 220; i++ {
		body.WriteString("\t_ = 1\n")
	}
	body.WriteString("}\n")
	if err := os.WriteFile(filepath.Join(root, "long.go"), []byte(body.String()), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	result, err := scanRoots([]string{filepath.Join(dir, "internal")})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	found := false
	for _, item := range result.Violations {
		if item.Rule == "function-lines" {
			found = true
		}
	}
	if !found {
		t.Errorf("scan did not report function-lines; violations were %+v", result.Violations)
	}
}

// A clean file must produce nothing. Without this, a scan that reported
// everything would pass every test above.
func TestScanIsQuietOnCleanCode(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "internal", "clean")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	source := "package clean\n\nfunc tidy(a int) int { return a + 1 }\n"
	if err := os.WriteFile(filepath.Join(root, "clean.go"), []byte(source), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	result, err := scanRoots([]string{filepath.Join(dir, "internal")})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(result.Violations) != 0 {
		t.Errorf("clean code reported %+v", result.Violations)
	}
}

// The checked-in baseline must describe the tree it is checked into. A stale
// entry here is not a style problem: it is the gate reporting a debt that no
// longer exists, which is the signal that lowers the floor.
func TestCheckedInBaselineMatchesTheTree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	t.Chdir(filepath.Join("..", ".."))
	data, err := os.ReadFile(filepath.Join("scripts", "baselines", "go-quality.json"))
	if err != nil {
		t.Fatalf("read baseline: %v", err)
	}
	var baseline report
	if err := json.Unmarshal(data, &baseline); err != nil {
		t.Fatalf("decode baseline: %v", err)
	}
	if baseline.Version != baselineVersion {
		t.Fatalf("baseline version = %d, want %d", baseline.Version, baselineVersion)
	}
	current, err := scan()
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if problems := ratchetProblems(current, baseline); len(problems) > 0 {
		t.Errorf("the checked-in baseline does not match the tree: %v", problems)
	}
}
