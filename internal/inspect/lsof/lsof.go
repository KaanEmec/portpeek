// Package lsof is the macOS inspection adapter. It finds port owners with
// `lsof -F` machine-readable output and enriches each owning process with
// `ps` (full command) and `lsof -d cwd` (working directory).
package lsof

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
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

// Runner runs a system command and reports its output. A non-zero exit is
// reported through exitCode with a nil err; err is non-nil only when the
// command could not be started or did not exit normally.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (stdout, stderr []byte, exitCode int, err error)
}

// Inspector finds port owners on macOS. It implements inspect.Inspector.
type Inspector struct {
	runner Runner
}

var _ inspect.Inspector = (*Inspector)(nil)

// New returns an Inspector that runs the real system commands.
func New() *Inspector {
	return NewWithRunner(execRunner{})
}

// NewWithRunner returns an Inspector that runs commands through r.
func NewWithRunner(r Runner) *Inspector {
	return &Inspector{runner: r}
}

// Inspect reports every process owning q.Port. Zero owners with a nil error
// means no matching socket. Enrichment failures never fail the inspection;
// they are recorded in each Process.Unavailable instead.
func (i *Inspector) Inspect(ctx context.Context, q inspect.Query) (inspect.Result, error) {
	if err := q.Validate(); err != nil {
		return inspect.Result{}, fmt.Errorf("invalid query: %w", err)
	}

	stdout, stderr, code, err := i.runner.Run(ctx, "lsof", findArgs(q)...)
	if err != nil {
		return inspect.Result{}, runError("lsof", err)
	}
	// lsof exits 1 with no output when nothing matches; a failure is
	// distinguished by stderr text or another exit code. Warnings (for
	// example about an inaccessible file system) do not signal a failure.
	noMatch := code == 1 && withoutWarnings(stderr) == ""
	if code != 0 && !noMatch {
		return inspect.Result{}, exitError("lsof", code, stderr)
	}

	records, err := parseRecords(stdout)
	if err != nil {
		return inspect.Result{}, &inspect.Error{
			Kind: inspect.KindCommandFailed,
			Op:   "lsof",
			Err:  fmt.Errorf("parsing output: %w", err),
		}
	}

	// Enrichment failures are recorded per field, so a cancelled context
	// would otherwise surface as a result full of "unavailable" fields.
	if err := ctx.Err(); err != nil {
		return inspect.Result{}, err
	}
	owners := buildOwners(records, q)
	for idx := range owners {
		i.enrich(ctx, &owners[idx].Process)
	}
	if err := ctx.Err(); err != nil {
		return inspect.Result{}, err
	}
	return inspect.Result{Query: q, Owners: owners}, nil
}

func findArgs(q inspect.Query) []string {
	selector := string(q.Protocol) + ":" + strconv.Itoa(q.Port)
	return []string{"-nP", "-F", "pcnLTtfP0", "-i", selector}
}

// withoutWarnings returns stderr without lsof warnings, trimmed. A warning
// is a line containing "WARNING" plus any indented continuation lines, as in
//
//	lsof: WARNING: can't stat() nfs file system /Volumes/share
//	      Output information may be incomplete.
func withoutWarnings(stderr []byte) string {
	kept := []string{}
	inWarning := false
	for line := range strings.Lines(string(stderr)) {
		continuation := strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")
		switch {
		case strings.Contains(line, "WARNING"):
			inWarning = true
		case inWarning && continuation:
			// Part of the warning above.
		default:
			inWarning = false
			kept = append(kept, line)
		}
	}
	return strings.TrimSpace(strings.Join(kept, ""))
}

// enrich fills Command and WorkingDir, marking each field unavailable with a
// reason when it cannot be read. The cwd lookup runs first so that a
// process exiting mid-enrichment is detected by the later ps call.
func (i *Inspector) enrich(ctx context.Context, p *inspect.Process) {
	pid := strconv.Itoa(p.PID)

	cwd, cwdReason := i.lookupWorkingDir(ctx, pid)
	command, cmdReason := i.lookupCommand(ctx, pid)

	if cmdReason != "" {
		p.MarkUnavailable(inspect.FieldCommand, cmdReason)
	}
	p.Command = command

	if cwdReason != "" {
		if cmdReason == reasonExited {
			cwdReason = reasonExited
		}
		p.MarkUnavailable(inspect.FieldWorkingDir, cwdReason)
	}
	p.WorkingDir = cwd
}

// lookupCommand returns the full command line of pid, or a reason it could
// not be read.
func (i *Inspector) lookupCommand(ctx context.Context, pid string) (string, string) {
	stdout, stderr, code, err := i.runner.Run(ctx, "ps", "-o", "command=", "-p", pid)
	if err != nil {
		return "", reasonUnknown
	}
	command := strings.TrimSpace(string(stdout))
	switch {
	case command != "" && code == 0:
		return command, ""
	case isPermissionError(stderr):
		return "", reasonPermission
	case code == 1 && len(bytes.TrimSpace(stderr)) == 0:
		// ps exits 1 silently when no process has the PID.
		return "", reasonExited
	default:
		return "", reasonUnknown
	}
}

// lookupWorkingDir returns the working directory of pid, or a reason it
// could not be read.
func (i *Inspector) lookupWorkingDir(ctx context.Context, pid string) (string, string) {
	stdout, stderr, code, err := i.runner.Run(ctx, "lsof", "-nP", "-F", "n", "-d", "cwd", "-a", "-p", pid)
	if err != nil {
		return "", reasonUnknown
	}
	if code == 0 {
		records, perr := parseRecords(stdout)
		if dir := workingDir(records); perr == nil && dir != "" {
			return dir, ""
		}
		return "", reasonUnknown
	}
	switch {
	case isPermissionError(stderr):
		return "", reasonPermission
	case code == 1 && len(bytes.TrimSpace(stderr)) == 0:
		// Observed: lsof exits 1 silently for a live process owned by
		// another user. The caller upgrades this to "process exited" when
		// ps finds no such PID.
		return "", reasonPermission
	default:
		return "", reasonUnknown
	}
}

func isPermissionError(stderr []byte) bool {
	s := string(stderr)
	return strings.Contains(s, "Permission denied") || strings.Contains(s, "Operation not permitted")
}

// runError classifies a failure to run a command at all.
func runError(op string, err error) error {
	kind := inspect.KindCommandFailed
	if errors.Is(err, exec.ErrNotFound) {
		kind = inspect.KindToolMissing
	}
	return &inspect.Error{Kind: kind, Op: op, Err: err}
}

// exitError classifies a command that ran and exited unsuccessfully.
func exitError(op string, code int, stderr []byte) error {
	kind := inspect.KindCommandFailed
	if isPermissionError(stderr) {
		kind = inspect.KindPermissionDenied
	}
	msg := strings.TrimSpace(string(stderr))
	if msg == "" {
		return &inspect.Error{Kind: kind, Op: op, Err: fmt.Errorf("exit status %d", code)}
	}
	return &inspect.Error{Kind: kind, Op: op, Err: fmt.Errorf("exit status %d: %s", code, msg)}
}

// execRunner runs commands with os/exec.
type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) ([]byte, []byte, int, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, nil, -1, fmt.Errorf("running %s: %w", name, ctxErr)
	}
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok && exitErr.Exited() {
		return stdout.Bytes(), stderr.Bytes(), exitErr.ExitCode(), nil
	}
	if err != nil {
		return nil, nil, -1, fmt.Errorf("running %s: %w", name, err)
	}
	return stdout.Bytes(), stderr.Bytes(), 0, nil
}
