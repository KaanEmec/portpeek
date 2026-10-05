// Package cli implements the portpeek command line: argument parsing, exit
// codes, and rendering of inspection results as text or JSON.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/kaanemec/portpeek/internal/inspect"
)

// Exit codes returned by Run.
const (
	exitOK       = 0 // at least one owner found
	exitNoMatch  = 1 // no matching socket
	exitBadInput = 2 // invalid input or usage
	exitFailure  = 3 // inspection failed

	exitNotStopped = 4 // --stop given but nothing was stopped, or signalling failed

	exitInterrupted = 130 // interrupted; shell convention of 128 + SIGINT
)

// Version is the portpeek version, overridden at build time with -ldflags.
var Version = "dev"

const usageHint = "Try 'portpeek --help' for usage."

const helpText = `Usage: portpeek                       open the port overview (TUI)
       portpeek <port> [--tcp|--udp] [--detail|--json]
       portpeek <port> --stop [--pid <pid>] [--force] [--tcp|--udp]
       portpeek tui [--interval <duration>]   same as plain portpeek

Show which process is using a local port. Without a port, portpeek opens a
searchable, refreshing overview of every local port.

Options:
  --tcp        only look at TCP sockets
  --udp        only look at UDP sockets
  --detail     show everything known: every socket, user, command,
               working directory and stop commands
  --json       print machine-readable JSON (schema 1); --detail is ignored
  --stop       send SIGTERM to the process using the port, after confirmation
  --pid <pid>  with --stop, the process to stop when several use the port
  --force      with --stop, skip confirmation (required when stdin is not a terminal)
  --version    print the version and exit
  -h, --help   show this help and exit

Overview options (portpeek, portpeek tui):
  --interval <duration>  auto-refresh period, e.g. 10s (default 5s, minimum 1s)

--stop re-inspects the port right before signalling and stops nothing if the
process changed. It sends SIGTERM only and never escalates to SIGKILL.

Exit codes:
  0  at least one process uses the port (with --stop: SIGTERM was sent)
  1  no listening or bound socket on the port
  2  invalid input
  3  inspection failed (tool missing, permission denied, command error)
  4  --stop did not stop anything (declined, ambiguous, unknown owner, changed,
     or signal failed)
130  interrupted
`

// options holds the parsed command line.
type options struct {
	port     int
	protocol inspect.Protocol
	json     bool
	detail   bool
	version  bool
	help     bool

	stop  bool
	force bool
	pid   int // 0 when --pid was not given
}

// Deps are the platform pieces Run works with.
type Deps struct {
	// Inspector answers one-port queries.
	Inspector inspect.Inspector
	// Lister enumerates every local port; only the overview uses it.
	Lister inspect.Lister
	// TUI runs the terminal interface of the overview. It is injected
	// because the interface itself builds on this package.
	TUI func(ctx context.Context, opts TUIOptions) error

	// interactive reports whether stdin and stdout are both terminals. Nil
	// means interactiveTerminal; tests replace it.
	interactive func() bool
}

// Run parses args (without the program name), runs the inspector, writes the
// output, and returns the process exit code. Without a port, or with the tui
// subcommand, it opens the overview instead. Only --stop and the stop action
// of the overview have side effects; otherwise Run is read-only.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer, deps Deps) int {
	out := stdio{stdout: stdout, stderr: stderr}
	if len(args) > 0 && args[0] == tuiCommand {
		return runTUI(ctx, args[1:], out, deps)
	}

	overview, err := isOverview(args)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "portpeek: %v\n%s\n", err, usageHint)
		return exitBadInput
	}
	if overview {
		return runOverview(ctx, args, out, deps)
	}
	return run(ctx, args, out, deps.Inspector, systemStopper())
}

// stdio groups the output streams Run writes to.
type stdio struct {
	stdout io.Writer
	stderr io.Writer
	// width overrides the detected text width when positive.
	width int
}

// run is Run with the stop side effects injected, so tests can replace them.
func run(ctx context.Context, args []string, out stdio, ins inspect.Inspector, sys stopper) int {
	stdout, stderr := out.stdout, out.stderr
	opts, err := parseArgs(args)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "portpeek: %v\n%s\n", err, usageHint)
		return exitBadInput
	}

	switch {
	case opts.help:
		_, _ = fmt.Fprint(stdout, helpText)
		return exitOK
	case opts.version:
		_, _ = fmt.Fprintf(stdout, "portpeek %s\n", Version)
		return exitOK
	}

	q := inspect.Query{Port: opts.port, Protocol: opts.protocol}
	res, err := ins.Inspect(ctx, q)
	if errors.Is(err, context.Canceled) {
		return reportInterrupted(stderr)
	}
	if err != nil {
		return reportFailure(stdout, stderr, q, err, opts.json)
	}

	f := format{json: opts.json, detail: opts.detail, view: stdoutView(out)}
	if err := render(stdout, q, res.Owners, f); err != nil {
		_, _ = fmt.Fprintf(stderr, "portpeek: writing output: %v\n", err)
		return exitFailure
	}
	if len(res.Owners) == 0 {
		return exitNoMatch
	}
	if !opts.stop {
		return exitOK
	}

	s := stopRun{sys: sys, ins: ins, out: out, query: q}
	return s.stop(ctx, res.Owners, opts)
}

// reportInterrupted writes the interruption notice and returns its exit code.
func reportInterrupted(stderr io.Writer) int {
	_, _ = fmt.Fprintln(stderr, "portpeek: interrupted")
	return exitInterrupted
}

// parseArgs parses flags and the single positional port.
func parseArgs(args []string) (options, error) {
	var opts options
	var tcp, udp bool
	fs := newPortFlagSet(&opts, &tcp, &udp)

	positional, err := parseInterspersed(fs, args)
	if errors.Is(err, flag.ErrHelp) {
		return options{help: true}, nil
	}
	if err != nil {
		return options{}, err
	}

	if opts.version {
		return options{version: true}, nil
	}

	switch {
	case tcp && udp:
		return options{}, errors.New("--tcp and --udp cannot be used together")
	case tcp:
		opts.protocol = inspect.TCP
	case udp:
		opts.protocol = inspect.UDP
	}

	switch {
	case opts.stop && opts.json:
		return options{}, errors.New("--stop cannot be used with --json")
	case opts.force && !opts.stop:
		return options{}, errors.New("--force requires --stop")
	case opts.pid != 0 && !opts.stop:
		return options{}, errors.New("--pid requires --stop")
	}

	port, err := parsePort(positional)
	if err != nil {
		return options{}, err
	}
	opts.port = port
	return opts, nil
}

// newPortFlagSet returns the flag set of the one-port form, which stores the
// flag values in opts, tcp and udp.
func newPortFlagSet(opts *options, tcp, udp *bool) *flag.FlagSet {
	fs := flag.NewFlagSet("portpeek", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(tcp, "tcp", false, "")
	fs.BoolVar(udp, "udp", false, "")
	fs.BoolVar(&opts.json, "json", false, "")
	fs.BoolVar(&opts.detail, "detail", false, "")
	fs.BoolVar(&opts.version, "version", false, "")
	fs.BoolVar(&opts.stop, "stop", false, "")
	fs.BoolVar(&opts.force, "force", false, "")
	fs.Func("pid", "", func(value string) error {
		pid, err := strconv.Atoi(value)
		if err != nil || pid < 1 {
			return errors.New("must be a positive process ID")
		}
		opts.pid = pid
		return nil
	})
	return fs
}

// parseInterspersed parses args with fs and returns the positional
// arguments. Flags may appear before or after a positional argument, which
// the standard flag package does not allow on its own, so parsing is
// repeated after each one. Everything after a literal "--" is positional.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	if i := slices.Index(args, "--"); i >= 0 {
		positional = append(positional, args[i+1:]...)
		args = args[:i]
	}

	var leading []string
	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			return nil, err
		}
		rest = fs.Args()
		if len(rest) == 0 {
			break
		}
		leading = append(leading, rest[0])
		rest = rest[1:]
	}
	return append(leading, positional...), nil
}

// parsePort validates the positional arguments as exactly one port number.
func parsePort(positional []string) (int, error) {
	switch len(positional) {
	case 0:
		return 0, errors.New("missing port argument")
	case 1:
	default:
		return 0, fmt.Errorf("expected one port, got %d arguments", len(positional))
	}

	port, err := strconv.Atoi(positional[0])
	if err != nil {
		return 0, fmt.Errorf("invalid port %q: must be a number between 1 and 65535", positional[0])
	}
	if port < 1 || port > 65535 {
		return 0, fmt.Errorf("invalid port %d: must be between 1 and 65535", port)
	}
	return port, nil
}

// reportFailure writes an inspection error and returns exitFailure. With
// --json the error goes to stdout as a JSON document, otherwise to stderr.
func reportFailure(stdout, stderr io.Writer, q inspect.Query, err error, asJSON bool) int {
	kind, lines := describeError(q, err)
	if asJSON {
		if jerr := writeJSONError(stdout, kind, lines[0]); jerr != nil {
			_, _ = fmt.Fprintf(stderr, "portpeek: writing output: %v\n", jerr)
		}
		return exitFailure
	}

	_, _ = fmt.Fprintf(stderr, "portpeek: %s\n", lines[0])
	for _, line := range lines[1:] {
		_, _ = fmt.Fprintf(stderr, "%s\n", line)
	}
	return exitFailure
}

// ErrorText returns the friendly message the CLI prints for an inspection
// error, joined into one line. A query with port 0 stands for listing every
// local port, as the terminal interface does.
func ErrorText(q inspect.Query, err error) string {
	_, lines := describeError(q, err)
	return strings.Join(lines, " ")
}

// describeError maps an inspection error to a stable kind string and a
// one or two line friendly message. The slice always has at least one line.
// Port 0 in q means the error came from listing all ports.
func describeError(q inspect.Query, err error) (string, []string) {
	var ie *inspect.Error
	if !errors.As(err, &ie) {
		return "unknown", []string{err.Error()}
	}

	listing := q.Port == 0
	switch ie.Kind {
	case inspect.KindToolMissing:
		install := "Install it and make sure it is on your PATH."
		if ie.Op == "ss" {
			install = "Install it (part of the iproute2 package) and make sure it is on your PATH."
		}
		consequence := fmt.Sprintf("port %d cannot be inspected", q.Port)
		if listing {
			consequence = "local ports cannot be listed"
		}
		return string(ie.Kind), []string{
			fmt.Sprintf("required tool %q is not installed, so %s.", ie.Op, consequence),
			install,
		}
	case inspect.KindPermissionDenied:
		if listing {
			return string(ie.Kind), []string{
				"permission denied while listing local ports.",
				"Re-run with sudo for full details: sudo portpeek tui",
			}
		}
		return string(ie.Kind), []string{
			fmt.Sprintf("permission denied while inspecting port %d.", q.Port),
			fmt.Sprintf("Re-run with sudo for full details: sudo portpeek %d", q.Port),
		}
	default:
		return string(ie.Kind), []string{err.Error()}
	}
}
