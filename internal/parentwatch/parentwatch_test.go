package parentwatch_test

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/chester-hill-solutions/stow/internal/parentwatch"
)

func TestValidateRejectsAPidThatIsNotOurParent(t *testing.T) {
	// Our real parent pid is accepted when the getter agrees with it.
	if err := (parentwatch.Requested{Pid: os.Getppid(), Getppid: os.Getppid}).Validate(); err != nil {
		t.Fatalf("validating the real parent pid: %v", err)
	}
	// A pid that disagrees with the getter is refused: watching a stranger would
	// leave this process appearing protected while nothing is being watched.
	if err := (parentwatch.Requested{Pid: os.Getppid() + 1, Getppid: os.Getppid}).Validate(); !errors.Is(err, parentwatch.ErrNotParent) {
		t.Fatalf("mismatched pid error = %v, want ErrNotParent", err)
	}
	for _, pid := range []int{0, -1} {
		if err := (parentwatch.Requested{Pid: pid, Getppid: os.Getppid}).Validate(); !errors.Is(err, parentwatch.ErrNotParent) {
			t.Fatalf("pid %d error = %v, want ErrNotParent", pid, err)
		}
	}
}

// TestWatchRejectsForeignPid proves the server refuses to arm a watch on a pid
// that is not its parent, which is the failure that would otherwise leave a
// server running with no safety net while logging that it was protected.
func TestWatchRejectsForeignPid(t *testing.T) {
	err := parentwatch.Watch(parentwatch.Requested{Pid: os.Getppid() + 1, Getppid: os.Getppid})
	if !errors.Is(err, parentwatch.ErrNotParent) {
		t.Fatalf("Watch error = %v, want ErrNotParent", err)
	}
}

// TestWatchArmsForTheRealParent exercises the success path in this process.
// The kill test proves the signal arrives, but it runs the watch in a spawned
// process whose coverage is not collected, so without this the mechanism's own
// setup would go unmeasured.
func TestWatchArmsForTheRealParent(t *testing.T) {
	if err := parentwatch.Watch(parentwatch.Requested{Pid: os.Getppid(), Getppid: os.Getppid}); err != nil {
		t.Fatalf("arming the watch for the real parent: %v", err)
	}
}

// TestChildExitsWhenParentIsKilled is the case the graceful lifecycle test
// cannot reach: the parent is SIGKILLed, so no close handler runs.
func TestChildExitsWhenParentIsKilled(t *testing.T) {
	if os.Getenv("STOW_PARENTWATCH_CHILD") == "1" {
		// This process stands in for the server. Arm the watch against its real
		// parent, publish that it is armed, and stay alive until the kernel
		// delivers the signal. The parent kills its parent only after this file
		// appears, so the test never races process startup.
		if err := parentwatch.Watch(parentwatch.Requested{Pid: os.Getppid(), Getppid: os.Getppid}); err != nil {
			os.Exit(3)
		}
		if armed := os.Getenv("STOW_PARENTWATCH_ARMED_FILE"); armed != "" {
			_ = os.WriteFile(armed, []byte(strconv.Itoa(os.Getpid())), 0o600)
		}
		for {
			time.Sleep(50 * time.Millisecond)
		}
	}

	// The watched process needs a parent distinct from this test, otherwise
	// killing that parent kills the test too. An intermediate shell stays alive
	// as the parent and publishes the watched pid so it can be checked.
	dir := t.TempDir()
	logFile := filepath.Join(dir, "watched.log")
	armedFile := filepath.Join(dir, "watched.armed")
	script := fmt.Sprintf(
		"STOW_PARENTWATCH_CHILD=1 STOW_PARENTWATCH_ARMED_FILE=%s %s -test.run=TestChildExitsWhenParentIsKilled -test.timeout=60s > %s 2>&1 & wait",
		armedFile,
		os.Args[0],
		logFile,
	)
	intermediate := exec.Command("/bin/sh", "-c", script)
	intermediate.Env = append(os.Environ(), "STOW_PARENTWATCH_CHILD=0")
	if err := intermediate.Start(); err != nil {
		t.Fatalf("start intermediate parent: %v", err)
	}
	defer func() {
		_ = intermediate.Process.Kill()
		_ = intermediate.Wait()
	}()

	watchedPid := waitForPid(t, armedFile)
	// SIGKILL is the case that matters: the parent gets no chance to clean up,
	// so only the watch can save the child.
	if err := intermediate.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatalf("kill parent: %v", err)
	}
	_ = intermediate.Wait()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		// Signal 0 succeeds only while the process still exists.
		if err := syscall.Kill(watchedPid, 0); err != nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = syscall.Kill(watchedPid, syscall.SIGKILL)
	log, _ := os.ReadFile(logFile)
	t.Fatalf(
		"watched process %d was still alive 10s after its parent was killed; child output:\n%s",
		watchedPid,
		log,
	)
}

// waitForPid waits for the watched process to publish its pid, which it only
// does after the watch is armed. Waiting for this rather than for the fork
// removes a startup race that made the test flaky under load.
func waitForPid(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			if pid, convErr := strconv.Atoi(strings.TrimSpace(string(data))); convErr == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("the watched process never armed the watch within 30s")
	return 0
}
