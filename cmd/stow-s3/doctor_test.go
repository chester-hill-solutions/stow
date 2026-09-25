package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/chester-hill-solutions/stow-s3/internal/ready"
	"github.com/chester-hill-solutions/stow-s3/internal/version"
)

func findCheck(report doctorReport, name string) (doctorCheck, bool) {
	for _, check := range report.Checks {
		if check.Name == name {
			return check, true
		}
	}
	return doctorCheck{}, false
}

func TestCollectDoctorReportPassesEveryRequiredCheck(t *testing.T) {
	report, err := collectDoctorReport()
	if err != nil {
		t.Fatalf("collecting report: %v", err)
	}
	if !report.OK {
		t.Fatalf("report.OK = false; checks: %+v", report.Checks)
	}
	if report.ProtocolVersion != ready.ProtocolVersion {
		t.Errorf("protocolVersion = %d, want %d", report.ProtocolVersion, ready.ProtocolVersion)
	}
	if report.BinaryVersion != version.Version {
		t.Errorf("binaryVersion = %q, want %q", report.BinaryVersion, version.Version)
	}
	for _, check := range report.Checks {
		if check.Required && !check.OK {
			t.Errorf("required check %q failed: %s", check.Name, check.Error)
		}
	}
}

// TestCollectDoctorReportCoversEveryPlannedFact anchors the command to the list
// in docs/agent-dx-plan.md section 6.3, so dropping a check fails here rather
// than silently shipping a diagnostic with a hole in it.
func TestCollectDoctorReportCoversEveryPlannedFact(t *testing.T) {
	report, err := collectDoctorReport()
	if err != nil {
		t.Fatalf("collecting report: %v", err)
	}
	want := []string{
		"binary.path",     // resolved binary path
		"binary.version",  // binary version
		"platform",        // supported platform
		"tempDir",         // writable temporary directory
		"backend",         // available backend
		"s3.upstream",     // available S3 client configuration
		"endpoint.bind",   // whether a test endpoint can bind
		"endpoint.health", // and answer health checks
	}
	for _, name := range want {
		if _, ok := findCheck(report, name); !ok {
			t.Errorf("report is missing the %q check", name)
		}
	}
}

// TestDoctorNeverPrintsCredentials is the security property that makes the
// report safe to paste into a bug tracker. A report that leaked a configured
// secret would be a worse bug than the one the user was trying to report.
func TestDoctorNeverPrintsCredentials(t *testing.T) {
	const secret = "super-secret-value-that-must-never-appear"
	t.Setenv("STOW_ENDPOINT", "https://s3.example.invalid")
	t.Setenv("STOW_ACCESS_KEY_ID", "AKIAEXAMPLE")
	t.Setenv("STOW_SECRET_ACCESS_KEY", secret)

	report, err := collectDoctorReport()
	if err != nil {
		t.Fatalf("collecting report: %v", err)
	}
	check, ok := findCheck(report, "s3.upstream")
	if !ok {
		t.Fatal("report is missing the s3.upstream check")
	}
	if !strings.Contains(check.Detail, "configured") {
		t.Fatalf("s3.upstream detail = %q, want it to report a configured upstream", check.Detail)
	}
	// The access key is an identifier rather than a secret, but a report has no
	// reason to carry it either.
	for _, forbidden := range []string{secret, "AKIAEXAMPLE"} {
		if strings.Contains(check.Detail, forbidden) || strings.Contains(check.Error, forbidden) {
			t.Errorf("s3.upstream check leaked %q: %+v", forbidden, check)
		}
	}

	var human bytes.Buffer
	renderDoctorText(&human, report)
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("encoding report: %v", err)
	}
	for _, output := range []string{human.String(), string(encoded)} {
		if strings.Contains(output, secret) {
			t.Errorf("doctor output leaked the configured secret:\n%s", output)
		}
	}
}

func TestDoctorJSONIsSelfDescribingAndValid(t *testing.T) {
	report, err := collectDoctorReport()
	if err != nil {
		t.Fatalf("collecting report: %v", err)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("encoding report: %v", err)
	}
	var decoded doctorReport
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("report is not valid JSON: %v", err)
	}
	if len(decoded.Checks) != len(report.Checks) {
		t.Fatalf("decoded %d checks, want %d", len(decoded.Checks), len(report.Checks))
	}
	if decoded.OK != report.OK {
		t.Errorf("decoded ok = %v, want %v", decoded.OK, report.OK)
	}
}

// TestOptionalCheckFailureDoesNotFailTheReport protects the common local-only
// user. Having no upstream S3 configured is a normal state, and a diagnostic
// that exits non-zero for it would be noise for most of its audience.
func TestOptionalCheckFailureDoesNotFailTheReport(t *testing.T) {
	t.Setenv("STOW_ENDPOINT", "https://s3.example.invalid")
	t.Setenv("STOW_ACCESS_KEY_ID", "AKIAEXAMPLE")
	t.Setenv("STOW_SECRET_ACCESS_KEY", "")
	// An incomplete upstream is exactly the state a user hits when they set
	// half the environment and wonder why nothing works.
	t.Setenv("S3_ENDPOINT", "")
	t.Setenv("S3_ACCESS_KEY_ID", "")
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")

	check := checkS3Configuration()
	if check.Required {
		t.Error("the s3.upstream check must never be required")
	}

	report := doctorReport{Checks: []doctorCheck{checkFailed(check, "", "upstream is incomplete")}}
	if report.OK {
		t.Fatal("a report containing a failed check must not be OK")
	}
	var human bytes.Buffer
	renderDoctorText(&human, report)
	if !strings.Contains(human.String(), "warn") {
		t.Errorf("optional failure should render as warn, got:\n%s", human.String())
	}
	if strings.Contains(human.String(), "required check(s) failed") {
		t.Errorf("an optional failure must not be counted as a required failure:\n%s", human.String())
	}
}

func TestRenderDoctorTextCountsRequiredFailures(t *testing.T) {
	report := doctorReport{
		ProtocolVersion: ready.ProtocolVersion,
		BinaryVersion:   version.Version,
		Checks: []doctorCheck{
			checkSucceeded(doctorCheck{Name: "ok", OK: true, Required: true}, "fine"),
			checkFailed(doctorCheck{Name: "broken", OK: true, Required: true}, "", "could not bind"),
		},
	}
	var human bytes.Buffer
	renderDoctorText(&human, report)
	output := human.String()
	if !strings.Contains(output, "FAIL") {
		t.Errorf("required failure should render as FAIL, got:\n%s", output)
	}
	if !strings.Contains(output, "1 required check(s) failed.") {
		t.Errorf("required failure count missing, got:\n%s", output)
	}
	if !strings.Contains(output, "could not bind") {
		t.Errorf("failure detail missing, got:\n%s", output)
	}
}

func TestIsSupportedPlatform(t *testing.T) {
	for _, platform := range supportedPlatforms {
		if !isSupportedPlatform(platform) {
			t.Errorf("%q should be supported", platform)
		}
	}
	for _, platform := range []string{"windows/amd64", "linux/riscv64", "freebsd/amd64", ""} {
		if isSupportedPlatform(platform) {
			t.Errorf("%q should not be supported", platform)
		}
	}
}
