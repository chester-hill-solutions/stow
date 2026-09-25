//go:build android || darwin || dragonfly || freebsd || illumos || ios || linux || netbsd || openbsd

package runthrough

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

func outboxOwnerAlive(owner string) bool {
	parts := strings.Split(owner, "-")
	if len(parts) < 2 {
		return false
	}
	pid, err := strconv.Atoi(parts[1])
	if err != nil || pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return process.Signal(syscall.Signal(0)) == nil
}
