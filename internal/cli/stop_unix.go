//go:build !windows

package cli

import (
	"errors"
	"os"
	"syscall"
)

// terminate sends SIGTERM to pid.
func terminate(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	defer func() { _ = proc.Release() }()
	return proc.Signal(syscall.SIGTERM)
}

// alive reports whether pid exists, using the null signal. EPERM means the
// process exists but belongs to someone else.
func alive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	defer func() { _ = proc.Release() }()
	err = proc.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}
