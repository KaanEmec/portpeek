//go:build windows

package cli

import "strconv"

// allSocketsVisible is always true on Windows: netstat lists every socket
// with its PID regardless of privilege. Details that need administrator
// rights are marked unavailable per field instead.
func allSocketsVisible() bool { return true }

// stopHint is the manual command a user can copy to stop a process.
func stopHint(pid int) string { return "taskkill /PID " + strconv.Itoa(pid) }
