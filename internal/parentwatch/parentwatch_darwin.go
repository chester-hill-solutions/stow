//go:build darwin

package parentwatch

import "syscall"

// Watch arms a kqueue filter that reports when the parent process exits.
//
// EVFILT_PROC with NOTE_EXIT watches the process identity rather than a pid
// number, so pid reuse cannot trigger a false exit, and the kernel delivers the
// event however the parent died.
func Watch(request Requested) error {
	if err := request.Validate(); err != nil {
		return err
	}
	descriptor, err := syscall.Kqueue()
	if err != nil {
		return err
	}
	defer syscall.Close(descriptor)

	change := syscall.Kevent_t{}
	change.Ident = uint64(request.Pid)
	change.Filter = int16(syscall.EVFILT_PROC)
	change.Fflags = syscall.NOTE_EXIT
	change.Flags = syscall.EV_ADD | syscall.EV_CLEAR

	registered, err := syscall.Kevent(descriptor, []syscall.Kevent_t{change}, nil, nil)
	if err != nil {
		return err
	}
	if registered == 0 {
		return ErrNotParent
	}
	return nil
}
