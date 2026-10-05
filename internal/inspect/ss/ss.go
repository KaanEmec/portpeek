// Package ss is the Linux inspection adapter. It finds port owners with
// `ss` (iproute2) and enriches each owning process from procfs: name from
// /proc/PID/comm, command from /proc/PID/cmdline, working directory from
// /proc/PID/cwd, and user from the Uid line of /proc/PID/status.
package ss

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

// Runner runs a system command and reports its output. A non-zero exit is
// reported through exitCode with a nil err; err is non-nil only when the
// command could not be started or did not exit normally.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (stdout, stderr []byte, exitCode int, err error)
}

// Inspector finds port owners on Linux. It implements inspect.Inspector.
type Inspector struct {
	runner Runner
	// procRoot is the procfs mount point; tests point it at a fixture tree.
	procRoot string
}

var _ inspect.Inspector = (*Inspector)(nil)

// New returns an Inspector that runs the real ss and reads /proc.
func New() *Inspector {
	return NewWithRunner(execRunner{})
}

// NewWithRunner returns an Inspector that runs commands through r and reads
// process details from /proc.
func NewWithRunner(r Runner) *Inspector {
	return &Inspector{runner: r, procRoot: "/proc"}
}

// Inspect reports every process owning q.Port. Zero owners with a nil error
// means no matching socket. Enrichment failures never fail the inspection;
// they are recorded in each Process.Unavailable instead.
func (i *Inspector) Inspect(ctx context.Context, q inspect.Query) (inspect.Result, error) {
	if err := q.Validate(); err != nil {
		return inspect.Result{}, fmt.Errorf("invalid query: %w", err)
	}

	stdout, stderr, code, err := i.runner.Run(ctx, "ss", findArgs(q)...)
	if err != nil {
		return inspect.Result{}, runError("ss", err)
	}
	// ss prints nothing and exits 0 when no socket matches the filter, so
	// any non-zero exit is a failure.
	if code != 0 {
		return inspect.Result{}, exitError("ss", code, stderr)
	}

	rows, err := parseRows(stdout, q.Protocol)
	if err != nil {
		return inspect.Result{}, &inspect.Error{
			Kind: inspect.KindCommandFailed,
			Op:   "ss",
			Err:  fmt.Errorf("parsing output: %w", err),
		}
	}

	// Enrichment failures are recorded per field, so a cancelled context
	// would otherwise surface as a result full of "unavailable" fields.
	if err := ctx.Err(); err != nil {
		return inspect.Result{}, err
	}
	owners := buildOwners(rows, q)
	for idx := range owners {
		if owners[idx].Process.PID != unknownPID {
			i.enrich(&owners[idx].Process)
		}
	}
	if err := ctx.Err(); err != nil {
		return inspect.Result{}, err
	}
	return inspect.Result{Query: q, Owners: owners}, nil
}

// findArgs builds `ss -H -a -n -p -t -u 'sport = :PORT'`, dropping -t or -u
// when the query names one protocol. -H drops the header, -a includes
// non-listening sockets (UDP, TCP bound without listen), -n keeps addresses
// and ports numeric, and -p adds the owning processes when they are visible.
func findArgs(q inspect.Query) []string {
	args := []string{"-H", "-a", "-n", "-p"}
	switch q.Protocol {
	case inspect.TCP:
		args = append(args, "-t")
	case inspect.UDP:
		args = append(args, "-u")
	default:
		args = append(args, "-t", "-u")
	}
	return append(args, "sport = :"+strconv.Itoa(q.Port))
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
