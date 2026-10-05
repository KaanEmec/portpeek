// Package netstat is the Windows inspection adapter. It finds port owners
// with `netstat -a -n -o` and enriches each owning process with one
// PowerShell Get-CimInstance Win32_Process query: name, and command line or
// executable path. Windows offers no per-process working directory or user
// through that source, so those fields are always marked unavailable.
// Listing every local port uses the same netstat output and skips
// PowerShell.
//
// The package uses no Windows-only APIs, so its parsers and orchestration
// are tested on every OS; only the live test needs Windows.
package netstat

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/kaanemec/portpeek/internal/inspect"
)

// Runner runs a system command and reports its output. A non-zero exit is
// reported through exitCode with a nil err; err is non-nil only when the
// command could not be started or did not exit normally.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (stdout, stderr []byte, exitCode int, err error)
}

// Inspector finds port owners on Windows. It implements inspect.Inspector and
// inspect.Lister.
type Inspector struct {
	runner Runner
}

var (
	_ inspect.Inspector = (*Inspector)(nil)
	_ inspect.Lister    = (*Inspector)(nil)
)

// New returns an Inspector that runs the real netstat and powershell.
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

	rows, err := i.findSockets(ctx, tables(q.Protocol))
	if err != nil {
		return inspect.Result{}, err
	}

	// Enrichment failures are recorded per field, so a cancelled context
	// would otherwise surface as a result full of "unavailable" fields.
	if err := ctx.Err(); err != nil {
		return inspect.Result{}, err
	}
	owners := buildOwners(rows, q)
	for idx := range owners {
		if !needsEnrichment(owners[idx].Process.PID) {
			continue
		}
		// Each enrichment starts a PowerShell process; stop starting them
		// once the inspection is interrupted.
		if err := ctx.Err(); err != nil {
			return inspect.Result{}, err
		}
		i.enrich(ctx, &owners[idx].Process)
	}
	if err := ctx.Err(); err != nil {
		return inspect.Result{}, err
	}
	return inspect.Result{Query: q, Owners: owners}, nil
}

// List reports every process holding a listening TCP or bound UDP socket,
// on any port. netstat reports only PIDs and PowerShell is not run, so Name
// is left unset except for the System process, and Command and WorkingDir
// are left unset. User stays marked unavailable, as in Inspect, because no
// Windows source this adapter uses reports it. Sockets netstat reports with
// PID 0 are grouped under the unknown owner, as in Inspect.
func (i *Inspector) List(ctx context.Context) (inspect.Snapshot, error) {
	taken := time.Now()

	rows, err := i.findSockets(ctx, tables(""))
	if err != nil {
		return inspect.Snapshot{}, err
	}
	if err := ctx.Err(); err != nil {
		return inspect.Snapshot{}, err
	}
	// The zero Query matches every port and protocol.
	owners := buildOwners(rows, inspect.Query{})
	for idx := range owners {
		// A snapshot leaves Command and WorkingDir unset rather than
		// unavailable.
		p := &owners[idx].Process
		delete(p.Unavailable, inspect.FieldCommand)
		delete(p.Unavailable, inspect.FieldWorkingDir)
	}
	return inspect.Snapshot{Taken: taken, Owners: owners}, nil
}

// findSockets runs netstat once per protocol table and parses the combined
// output. No socket yields no rows and a nil error.
func (i *Inspector) findSockets(ctx context.Context, tables []string) ([]row, error) {
	rows := []row{}
	for _, table := range tables {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		stdout, stderr, code, err := i.runner.Run(ctx, "netstat", findArgs(table)...)
		if err != nil {
			return nil, runError("netstat", err)
		}
		// netstat exits 0 and prints only its header when no socket
		// exists, so any non-zero exit is a failure.
		if code != 0 {
			return nil, exitError("netstat", code, stderr)
		}

		parsed, err := parseRows(stdout)
		if err != nil {
			return nil, &inspect.Error{
				Kind: inspect.KindCommandFailed,
				Op:   "netstat",
				Err:  fmt.Errorf("parsing %s output: %w", table, err),
			}
		}
		rows = append(rows, parsed...)
	}
	return rows, nil
}

// tables returns the netstat protocol tables a query needs. netstat -p takes
// a single protocol, and "TCP" and "UDP" cover IPv4 only.
func tables(p inspect.Protocol) []string {
	switch p {
	case inspect.TCP:
		return []string{"TCP", "TCPv6"}
	case inspect.UDP:
		return []string{"UDP", "UDPv6"}
	default:
		return []string{"TCP", "TCPv6", "UDP", "UDPv6"}
	}
}

// findArgs builds `netstat -a -n -o -p TABLE`: -a includes listening and
// bound sockets, -n keeps addresses and ports numeric, -o adds the owning
// PID, and -p restricts the output to one protocol table.
func findArgs(table string) []string {
	return []string{"-a", "-n", "-o", "-p", table}
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
// netstat -a -n -o needs no elevation, so every failure is a command
// failure.
func exitError(op string, code int, stderr []byte) error {
	msg := strings.TrimSpace(string(stderr))
	if msg == "" {
		return &inspect.Error{Kind: inspect.KindCommandFailed, Op: op, Err: fmt.Errorf("exit status %d", code)}
	}
	return &inspect.Error{Kind: inspect.KindCommandFailed, Op: op, Err: fmt.Errorf("exit status %d: %s", code, msg)}
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
