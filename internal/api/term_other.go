//go:build !windows

package api

import (
	"errors"
	"os"
	"syscall"
)

// processAlive probes with signal 0, which the kernel answers ESRCH for once the
// process is gone. os.FindProcess always succeeds on Unix, so it proves nothing.
// EPERM means the process exists but belongs to someone else.
func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = p.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}
	var errno syscall.Errno
	return errors.As(err, &errno) && errno == syscall.EPERM
}

func terminate(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Signal(syscall.SIGTERM)
}
