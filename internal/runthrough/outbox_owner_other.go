//go:build !android && !darwin && !dragonfly && !freebsd && !illumos && !ios && !linux && !netbsd && !openbsd

package runthrough

func outboxOwnerAlive(string) bool { return false }
