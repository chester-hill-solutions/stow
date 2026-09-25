// Package parentwatch makes a child process exit when the parent that started
// it disappears.
//
// It is opt-in. A hand-run "stow serve" keeps running when its shell exits,
// exactly as it does today, because only a session passes a parent pid.
//
// Both supported implementations watch the parent process identity rather than
// a bare pid number: Linux uses prctl(PR_SET_PDEATHSIG), which the kernel
// delivers when the parent thread dies, and macOS uses kqueue EVFILT_PROC with
// NOTE_EXIT. Neither can be fooled by pid reuse, and neither depends on a
// polling loop that could be missed.
package parentwatch

import "errors"

// ErrUnsupported is returned on a platform with no supported mechanism. The
// caller is expected to report it and continue: the absence of a safety net is
// worth logging, not worth refusing to serve over.
var ErrUnsupported = errors.New("parent death watching is not supported on this platform")

// ErrNotParent is returned when the supplied pid is not this process's parent,
// which means the caller passed a stale or wrong pid. Starting anyway would
// leave the process watching a stranger.
var ErrNotParent = errors.New("the supplied pid is not this process's parent")

// Requested describes a parent the caller asked us to watch.
type Requested struct {
	// Pid is the parent process id supplied by the caller.
	Pid int
	// Getppid reports the current parent, used to reject a pid that is not us.
	Getppid func() int
}

// Validate rejects a request that cannot be honoured before any mechanism is
// installed, so a bad pid fails loudly instead of silently doing nothing.
func (r Requested) Validate() error {
	if r.Pid <= 0 {
		return ErrNotParent
	}
	if r.Getppid != nil && r.Getppid() != r.Pid {
		return ErrNotParent
	}
	return nil
}
