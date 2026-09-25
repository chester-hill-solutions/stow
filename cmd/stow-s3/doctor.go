package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/chester-hill-solutions/stow-s3/internal/auth"
	"github.com/chester-hill-solutions/stow-s3/internal/ready"
	"github.com/chester-hill-solutions/stow-s3/internal/runthrough"
	"github.com/chester-hill-solutions/stow-s3/internal/s3api"
	"github.com/chester-hill-solutions/stow-s3/internal/storage"
	"github.com/chester-hill-solutions/stow-s3/internal/storage/fs"
	"github.com/chester-hill-solutions/stow-s3/internal/version"
)

// doctorCheck is one reported fact.
//
// Required separates "this is broken" from "this capability is absent and the
// user may not need it". A local-only user has no upstream S3 configuration,
// which is a normal state rather than a problem, so marking every check
// required would make the command fail for most of its audience.
type doctorCheck struct {
	Name     string `json:"name"`
	OK       bool   `json:"ok"`
	Required bool   `json:"required"`
	Detail   string `json:"detail,omitempty"`
	Error    string `json:"error,omitempty"`
}

// doctorReport is the machine-readable result. It is a flat list of checks
// rather than a fixed object per subsystem so that a client wrapper can append
// its own client-side checks, which the binary cannot observe, and merge the two
// into one report without either side knowing the other's fields.
type doctorReport struct {
	ProtocolVersion int           `json:"protocolVersion"`
	BinaryVersion   string        `json:"binaryVersion"`
	OK              bool          `json:"ok"`
	Checks          []doctorCheck `json:"checks"`
}

// supportedPlatforms are the os/arch pairs the release publishes a native
// binary for. scripts/check-version.mjs cross-checks this list against the npm
// platform manifests, so it cannot silently drift from what actually ships.
var supportedPlatforms = []string{
	"darwin/amd64",
	"linux/amd64",
	"linux/arm64",
	"darwin/arm64",
}

// exit statuses are distinct so a caller can tell a broken environment from a
// doctor that could not run at all.
const (
	doctorExitFailedCheck = 1
	doctorExitCouldNotRun = 2
)

// doctor runs the diagnostic and exits with a status describing the result.
func doctor(args []string) {
	flags := flag.NewFlagSet("doctor", flag.ExitOnError)
	asJSON := flags.Bool("json", false, "Emit one machine-readable JSON object instead of the human-readable report")
	flags.Parse(args)

	report, err := collectDoctorReport()
	if err != nil {
		if *asJSON {
			fmt.Fprintf(os.Stdout, "{\"error\":%q,\"ok\":false}\n", err.Error())
		} else {
			fmt.Fprintf(os.Stderr, "stow doctor could not run: %v\n", err)
		}
		os.Exit(doctorExitCouldNotRun)
	}
	if *asJSON {
		if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
			fmt.Fprintf(os.Stderr, "stow doctor: encode report: %v\n", err)
			os.Exit(doctorExitCouldNotRun)
		}
	} else {
		renderDoctorText(os.Stdout, report)
	}
	if !report.OK {
		os.Exit(doctorExitFailedCheck)
	}
}

func collectDoctorReport() (doctorReport, error) {
	checks := []doctorCheck{
		checkBinaryPath(),
		checkBinaryVersion(),
		checkPlatform(),
		checkTempDir(),
		checkBackends(),
		checkS3Configuration(),
	}
	endpointChecks, err := checkEndpoint()
	if err != nil {
		return doctorReport{}, err
	}
	checks = append(checks, endpointChecks...)

	report := doctorReport{
		ProtocolVersion: ready.ProtocolVersion,
		BinaryVersion:   version.Version,
		Checks:          checks,
	}
	report.OK = true
	for _, check := range checks {
		if check.Required && !check.OK {
			report.OK = false
		}
	}
	return report, nil
}

func checkBinaryPath() doctorCheck {
	check := doctorCheck{Name: "binary.path", OK: true, Required: true}
	path, err := os.Executable()
	if err != nil {
		return checkFailed(check, "", "cannot determine own path")
	}
	return checkSucceeded(check, path)
}

func checkBinaryVersion() doctorCheck {
	return checkSucceeded(doctorCheck{Name: "binary.version", OK: true, Required: true}, version.Version)
}

func checkPlatform() doctorCheck {
	check := doctorCheck{Name: "platform", OK: true, Required: true}
	platform := runtime.GOOS + "/" + runtime.GOARCH
	if !isSupportedPlatform(platform) {
		// Not required: an unsupported platform still runs, it just has no
		// published binary, so this reports rather than fails.
		check.Required = false
		return checkSucceeded(check, platform+" (no published binary for this platform)")
	}
	return checkSucceeded(check, platform+" (supported)")
}

func isSupportedPlatform(platform string) bool {
	for _, supported := range supportedPlatforms {
		if supported == platform {
			return true
		}
	}
	return false
}

// checkTempDir proves the temporary directory is writable by actually writing,
// because a temp directory that exists but cannot be written fails a session
// later, at an unrelated-looking moment.
func checkTempDir() doctorCheck {
	check := doctorCheck{Name: "tempDir", OK: true, Required: true}
	file, err := os.CreateTemp(os.TempDir(), "stow-doctor-*")
	if err != nil {
		return checkFailed(check, os.TempDir(), err.Error())
	}
	name := file.Name()
	if _, err := file.WriteString("stow doctor"); err != nil {
		_ = file.Close()
		_ = os.Remove(name)
		return checkFailed(check, os.TempDir(), err.Error())
	}
	_ = file.Close()
	if err := os.Remove(name); err != nil {
		return checkFailed(check, os.TempDir(), err.Error())
	}
	return checkSucceeded(check, os.TempDir()+" is writable")
}

func checkBackends() doctorCheck {
	check := doctorCheck{Name: "backend", OK: true, Required: true}
	filesystemErr := probeFilesystemBackend()
	if filesystemErr != nil {
		return checkFailed(check, "", filesystemErr.Error())
	}
	return checkSucceeded(check, "memory and filesystem")
}

// probeFilesystemBackend opens a throwaway filesystem store and round-trips one
// object through it, in a temporary directory so doctor never touches the data
// directory a real server would use.
func probeFilesystemBackend() error {
	dir, err := os.MkdirTemp("", "stow-doctor-store-*")
	if err != nil {
		return fmt.Errorf("filesystem backend: create temporary directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	store, err := fs.NewFilesystemStore(dir)
	if err != nil {
		return fmt.Errorf("filesystem backend: open: %w", err)
	}
	ctx := context.Background()
	if err := store.CreateBucket(ctx, "doctor"); err != nil {
		return fmt.Errorf("filesystem backend: create bucket: %w", err)
	}
	if _, err := store.PutObject(ctx, "doctor", "probe", strings.NewReader("probe"), storage.PutOptions{}); err != nil {
		return fmt.Errorf("filesystem backend: write object: %w", err)
	}
	reader, _, err := store.GetObject(ctx, "doctor", "probe")
	if err != nil {
		return fmt.Errorf("filesystem backend: read object: %w", err)
	}
	if _, err := io.Copy(io.Discard, reader); err != nil {
		_ = reader.Close()
		return fmt.Errorf("filesystem backend: read object body: %w", err)
	}
	return reader.Close()
}

// checkS3Configuration reports whether an upstream is configured. It is never
// required, and it reports presence rather than values: a doctor report is
// meant to be pasted into a bug tracker, so a secret must never reach it.
func checkS3Configuration() doctorCheck {
	check := doctorCheck{Name: "s3.upstream", OK: true, Required: false}
	config, err := runthrough.ConfigFromEnvChecked()
	if err != nil {
		check.OK = false
		check.Error = err.Error()
		return check
	}
	endpoint := strings.TrimSpace(config.Upstream.Endpoint)
	accessKey := strings.TrimSpace(config.Upstream.AccessKey)
	secretKey := strings.TrimSpace(config.Upstream.SecretKey)
	if endpoint == "" || accessKey == "" || secretKey == "" {
		return checkSucceeded(check, "not configured (optional; needed only for run-through mode)")
	}
	return checkSucceeded(check, "configured for "+runthrough.RedactEndpoint(endpoint))
}

// doctorAuth accepts every request on the throwaway server that checkEndpoint
// starts. That server is bound to loopback on an ephemeral port, lives only for
// the length of one health request, and serves a memory store, so there is
// nothing to authenticate against. It accepts everything rather than carrying a
// generated credential, because a doctor report gets pasted into bug trackers
// and a throwaway secret is one more thing that could escape in a log.
func doctorAuth(*http.Request) error { return nil }

// silenceRequestLog suppresses the server's per-request log line for the
// duration of the probe and restores it afterwards. A request log for the
// doctor's own health check is noise that looks like a real problem, and it
// would interleave with the report. The process is short-lived and the
// original writer is restored, so nothing else is affected.
func silenceRequestLog() func() {
	previous := log.Writer()
	log.SetOutput(io.Discard)
	return func() { log.SetOutput(previous) }
}

// checkEndpoint starts a real server on an ephemeral port and asks its health
// route, which is the only way to know whether this machine can actually serve
// rather than merely whether it can start a process. The server uses a memory
// store and no authentication so the check creates no credentials to leak.
func checkEndpoint() ([]doctorCheck, error) {
	restoreLog := silenceRequestLog()
	defer restoreLog()
	server, err := s3api.New(s3api.Config{
		Store:            storage.NewMemoryStore(),
		Auth:             doctorAuth,
		Host:             "127.0.0.1",
		Port:             0,
		Region:           auth.DefaultRegion,
		Mode:             string(runthrough.ModeLocal),
		CachePolicy:      "none",
		WritePolicy:      "local-only",
		AllowPublicAdmin: false,
	})
	if err != nil {
		return nil, fmt.Errorf("endpoint: create server: %w", err)
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.ListenAndServe() }()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()

	address := server.Addr()
	if address == "" {
		// ListenAndServe may still be binding; give it the same short window
		// serve uses rather than reporting a false failure.
		deadline := time.Now().Add(2 * time.Second)
		for server.Addr() == "" && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		address = server.Addr()
	}
	if address == "" {
		return []doctorCheck{checkFailed(doctorCheck{Name: "endpoint.bind", OK: true, Required: true}, "", "could not bind a local port")}, nil
	}
	endpoint := "http://" + address
	bind := checkSucceeded(doctorCheck{Name: "endpoint.bind", OK: true, Required: true}, endpoint)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/_stow/health", nil)
	if err != nil {
		return []doctorCheck{bind, checkFailed(doctorCheck{Name: "endpoint.health", OK: true, Required: true}, endpoint, err.Error())}, nil
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return []doctorCheck{bind, checkFailed(doctorCheck{Name: "endpoint.health", OK: true, Required: true}, endpoint, err.Error())}, nil
	}
	defer func() { _ = response.Body.Close() }()
	_, _ = io.Copy(io.Discard, response.Body)
	health := doctorCheck{Name: "endpoint.health", OK: true, Required: true}
	if response.StatusCode != http.StatusOK {
		return []doctorCheck{bind, checkFailed(health, endpoint, fmt.Sprintf("GET /_stow/health returned %d", response.StatusCode))}, nil
	}
	return []doctorCheck{bind, checkSucceeded(health, endpoint+" answered 200")}, nil
}

func checkSucceeded(check doctorCheck, detail string) doctorCheck {
	check.OK = true
	check.Detail = detail
	return check
}

func checkFailed(check doctorCheck, detail, message string) doctorCheck {
	check.OK = false
	check.Detail = detail
	check.Error = message
	return check
}

// oneLine collapses whitespace so a multi-line error, such as a usage message
// captured from a child process, cannot break the alignment of the report.
func oneLine(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func renderDoctorText(out io.Writer, report doctorReport) {
	fmt.Fprintf(out, "stow doctor (protocol %d, binary %s)\n\n", report.ProtocolVersion, report.BinaryVersion)
	width := 0
	for _, check := range report.Checks {
		if len(check.Name) > width {
			width = len(check.Name)
		}
	}
	failures := 0
	for _, check := range report.Checks {
		status := "ok  "
		switch {
		case check.OK:
		case check.Required:
			status = "FAIL"
			failures++
		default:
			status = "warn"
		}
		line := fmt.Sprintf("  %s  %-*s  %s", status, width, check.Name, oneLine(check.Detail))
		if check.Error != "" {
			line += ": " + oneLine(check.Error)
		}
		fmt.Fprintln(out, strings.TrimRight(line, " "))
	}
	fmt.Fprintln(out)
	if failures == 0 {
		fmt.Fprintln(out, "No required checks failed.")
		return
	}
	fmt.Fprintf(out, "%d required check(s) failed.\n", failures)
}
