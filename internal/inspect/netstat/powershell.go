package netstat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/kaanemec/portpeek/internal/inspect"
)

// Reasons recorded in Process.Unavailable.
const (
	reasonExited        = "process exited"
	reasonUnknown       = "unavailable"
	reasonPrivileges    = "not readable without elevated privileges"
	reasonNotOnWindows  = "not available on Windows"
	reasonNotReported   = "not reported by netstat"
	reasonSystemProcess = "the System process has no command line"
)

// processInfo is the JSON object PowerShell prints for one Win32_Process.
// CommandLine and ExecutablePath are null for processes of other users
// unless the caller is elevated, which decodes as "".
type processInfo struct {
	Name           string
	CommandLine    string
	ExecutablePath string
}

// errProcessGone reports that Win32_Process had no entry for the PID: the
// process exited between netstat and enrichment.
var errProcessGone = errors.New("no such process")

// enrich fills Name and Command from one PowerShell Win32_Process query,
// marking each field unavailable with a reason when it cannot be read. A
// missing or failing PowerShell never fails the inspection.
func (i *Inspector) enrich(ctx context.Context, p *inspect.Process) {
	stdout, stderr, code, err := i.runner.Run(ctx, "powershell", processArgs(p.PID)...)
	if err == nil && code != 0 {
		err = fmt.Errorf("exit status %d", code)
	}
	var info processInfo
	if err == nil {
		info, err = parseProcessInfo(stdout, stderr)
	}

	switch {
	case errors.Is(err, errProcessGone):
		p.MarkUnavailable(inspect.FieldName, reasonExited)
		p.MarkUnavailable(inspect.FieldCommand, reasonExited)
		return
	case err != nil:
		p.MarkUnavailable(inspect.FieldName, reasonUnknown)
		p.MarkUnavailable(inspect.FieldCommand, reasonUnknown)
		return
	}

	p.Name = info.Name
	if p.Name == "" {
		p.MarkUnavailable(inspect.FieldName, reasonUnknown)
	}

	// Without elevation Windows hides the command line of other users'
	// processes; the executable path is sometimes still readable.
	switch {
	case info.CommandLine != "":
		p.Command = info.CommandLine
	case info.ExecutablePath != "":
		p.Command = info.ExecutablePath
	default:
		p.MarkUnavailable(inspect.FieldCommand, reasonPrivileges)
	}
}

// processArgs builds the PowerShell invocation that prints one process as
// compact JSON, or nothing when no process has the PID.
func processArgs(pid int) []string {
	script := fmt.Sprintf(
		"Get-CimInstance Win32_Process -Filter 'ProcessId = %d' | "+
			"Select-Object Name,CommandLine,ExecutablePath | ConvertTo-Json -Compress",
		pid,
	)
	return []string{"-NoProfile", "-NonInteractive", "-Command", script}
}

// parseProcessInfo decodes the PowerShell output. Empty output with nothing
// on stderr means the process no longer exists.
func parseProcessInfo(stdout, stderr []byte) (processInfo, error) {
	// Windows PowerShell may prefix redirected output with a UTF-8 BOM.
	out := bytes.TrimSpace(bytes.TrimPrefix(stdout, []byte("\xef\xbb\xbf")))
	if len(out) == 0 {
		if msg := strings.TrimSpace(string(stderr)); msg != "" {
			return processInfo{}, fmt.Errorf("powershell: %s", msg)
		}
		return processInfo{}, errProcessGone
	}
	var info processInfo
	if err := json.Unmarshal(out, &info); err != nil {
		return processInfo{}, fmt.Errorf("decoding powershell output: %w", err)
	}
	info.Name = strings.TrimSpace(info.Name)
	info.CommandLine = strings.TrimSpace(info.CommandLine)
	info.ExecutablePath = strings.TrimSpace(info.ExecutablePath)
	return info, nil
}
