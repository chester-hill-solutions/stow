//go:build linux

package parentwatch

import (
	"os"
	"syscall"
)

// PR_SET_PDEATHSIG is the prctl option that asks the kernel to signal this
// process when its parent dies. It is delivered by the kernel on the death of
// the parent thread regardless of how the parent died, so a SIGKILLed parent
// still takes the child with it, and the kernel tracks the real parent process
// rather than a pid number, so pid reuse cannot fool it.
const prSetPdeathsig = 1

// Watch installs the kernel parent-death signal. The caller is responsible for
// exiting when the signal arrives; this only arms the mechanism.
func Watch(request Requested) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if _, _, errno := syscall.RawSyscall6(
		syscall.SYS_PRCTL,
		prSetPdeathsig,
		uintptr(syscall.SIGTERM),
		0, 0, 0, 0,
	); errno != 0 {
		return errno
	}
	// A race is possible between reading the parent and arming the mechanism: if
	// the parent died in that window the signal has already been missed. Check
	// again and fail so the caller does not serve believing it is watched.
	if os.Getppid() != request.Pid {
		return ErrNotParent
	}
	return nil
}
