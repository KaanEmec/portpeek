package ss

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/kaanemec/portpeek/internal/inspect"
)

// Reasons recorded in Process.Unavailable.
const (
	reasonExited     = "process exited"
	reasonPermission = "permission denied"
	reasonUnknown    = "unavailable"
)

// lookupUsername resolves a numeric uid to a user name. It is a variable so
// tests can replace it.
var lookupUsername = func(uid string) (string, error) {
	u, err := user.LookupId(uid)
	if err != nil {
		return "", err
	}
	return u.Username, nil
}

// enrich fills Name, User, Command and WorkingDir from procfs, marking each
// field unavailable with a reason when it cannot be read. Name keeps the
// name ss reported when comm cannot be read. Whether the process is still
// alive is checked after every read, so a process exiting mid-enrichment is
// reported as exited rather than as a permission problem.
func (i *Inspector) enrich(p *inspect.Process) {
	dir := filepath.Join(i.procRoot, strconv.Itoa(p.PID))

	comm, commErr := readTrimmed(filepath.Join(dir, "comm"))
	command, cmdErr := readCommand(filepath.Join(dir, "cmdline"))
	cwd, cwdErr := os.Readlink(filepath.Join(dir, "cwd"))
	uid, uidErr := readUID(filepath.Join(dir, "status"))

	_, statErr := os.Stat(dir)
	exited := errors.Is(statErr, fs.ErrNotExist)

	switch {
	case commErr == nil:
		p.Name = comm
	case p.Name == "":
		p.MarkUnavailable(inspect.FieldName, unavailableReason(commErr, exited))
	}

	p.Command = command
	if cmdErr != nil {
		p.MarkUnavailable(inspect.FieldCommand, unavailableReason(cmdErr, exited))
	}

	p.WorkingDir = cwd
	if cwdErr != nil {
		p.MarkUnavailable(inspect.FieldWorkingDir, unavailableReason(cwdErr, exited))
	}

	if uidErr != nil {
		p.MarkUnavailable(inspect.FieldUser, unavailableReason(uidErr, exited))
		return
	}
	// A uid without a passwd entry (common in containers) is still a
	// truthful answer.
	p.User = uid
	if name, err := lookupUsername(uid); err == nil && name != "" {
		p.User = name
	}
}

// errEmpty reports a procfs file that exists but holds nothing, as cmdline
// does for a zombie or a process that is exiting.
var errEmpty = errors.New("empty")

// unavailableReason maps a procfs read error to a reason for
// Process.Unavailable.
func unavailableReason(err error, exited bool) string {
	switch {
	case exited:
		return reasonExited
	case errors.Is(err, fs.ErrPermission):
		return reasonPermission
	default:
		return reasonUnknown
	}
}

// readTrimmed returns the content of a one-line procfs file such as comm.
func readTrimmed(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	s := strings.TrimSpace(string(data))
	if s == "" {
		return "", errEmpty
	}
	return s, nil
}

// readCommand returns /proc/PID/cmdline with its NUL-separated arguments
// joined by spaces.
func readCommand(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	data = bytes.TrimRight(data, "\x00")
	if len(data) == 0 {
		return "", errEmpty
	}
	return string(bytes.ReplaceAll(data, []byte{0}, []byte{' '})), nil
}

// readUID returns the real uid from the "Uid:" line of /proc/PID/status,
// which lists the real, effective, saved and filesystem uids.
func readUID(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	for line := range strings.Lines(string(data)) {
		rest, ok := strings.CutPrefix(line, "Uid:")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			break
		}
		if _, err := strconv.Atoi(fields[0]); err != nil {
			return "", fmt.Errorf("invalid Uid line %q", strings.TrimSpace(line))
		}
		return fields[0], nil
	}
	return "", fmt.Errorf("no Uid line in %s", path)
}
