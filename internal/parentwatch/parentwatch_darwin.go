//go:build darwin

package parentwatch

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// Watch arms a kqueue EVFILT_PROC filter with NOTE_EXIT, which watches the
// process identity rather than a pid number so pid reuse cannot trigger a false
// exit, and blocks a goroutine on the kqueue for the life of the process.
//
// The descriptor is deliberately never closed. kqueue filters are scoped to the
// open descriptor, so closing it removes every filter registered against it. An
// earlier version registered the filter and then closed the descriptor as it
// returned, with no goroutine ever waiting on it, and separately read
// kevent()'s return value as a count of registered changes when it is a count
// of delivered events. Either defect alone is fatal, and both were present at
// once: the descriptor was dropped and the registration was reported as failed,
// so the watch was never armed on macOS at all. Nothing about the filter
// survives the descriptor, so the descriptor has to outlive this function.
//
// When the parent exits the goroutine raises SIGTERM on this process. That is
// the same disposition prctl(PR_SET_PDEATHSIG) produces on Linux, so both
// platforms terminate identically whatever the server's own signal handling is,
// and neither depends on a polling loop that could be missed.
func Watch(request Requested) error {
	if err := request.Validate(); err != nil {
		return err
	}
	descriptor, err := syscall.Kqueue()
	if err != nil {
		return err
	}

	change := syscall.Kevent_t{}
	change.Ident = uint64(request.Pid)
	change.Filter = int16(syscall.EVFILT_PROC)
	change.Fflags = syscall.NOTE_EXIT
	change.Flags = syscall.EV_ADD | syscall.EV_CLEAR

	// kevent() reports how many events it placed in the eventlist, NOT how many
	// changes it registered. A registration-only call passes no eventlist, so the
	// count is always 0 and treating 0 as failure rejects every pid, including
	// the real parent. A previous version of this function did exactly that, so
	// Watch returned ErrNotParent on every macOS call and the watch had never
	// once been armed here. Success is err == nil; a pid that does not exist
	// fails with ESRCH instead, which the caller should see.
	if _, err := syscall.Kevent(descriptor, []syscall.Kevent_t{change}, nil, nil); err != nil {
		syscall.Close(descriptor)
		return err
	}

	// The parent can die between Validate and the registration above. A filter
	// armed against a process that has already exited never fires, so the watch
	// would be armed, reported as armed, and never trigger. Re-check and refuse,
	// which is the same arm/death race the Linux implementation closes.
	if request.Getppid != nil && request.Getppid() != request.Pid {
		syscall.Close(descriptor)
		return ErrNotParent
	}

	go waitForParentExit(descriptor)

	return nil
}

// waitForParentExit blocks until the watched parent exits and then raises
// SIGTERM on this process. It owns the descriptor for the life of the process.
func waitForParentExit(descriptor int) {
	events := make([]syscall.Kevent_t, 1)
	for {
		// A nil changelist blocks until an event is delivered. EV_CLEAR resets
		// the filter after delivery, so a wakeup that carries nothing usable
		// simply re-arms rather than leaving the watch disarmed.
		delivered, err := syscall.Kevent(descriptor, nil, events, nil)
		if err != nil {
			if errors.Is(err, syscall.EINTR) {
				continue
			}
			// The watch was armed successfully and has now failed. Continuing
			// would leave a server that logs itself as protected and is not, so
			// stop loudly rather than serve on a safety net that is gone.
			fmt.Fprintf(os.Stderr, "stow: parent-death watch failed: %v\n", err)
			os.Exit(1)
		}
		if delivered > 0 {
			_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
			return
		}
	}
}
