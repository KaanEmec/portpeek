//go:build !windows

package cli

import (
	"os"
	"strconv"
)

// allSocketsVisible is true for root. On macOS, lsof silently omits other
// users' sockets unless run as root, so an unprivileged result may be
// incomplete and a "no match" may simply mean "not visible". Linux ss lists
// every socket but hides the owner, which the adapter reports as PID 0.
func allSocketsVisible() bool { return os.Geteuid() == 0 }

// stopHint is the manual command a user can copy to stop a process.
func stopHint(pid int) string { return "kill " + strconv.Itoa(pid) }
