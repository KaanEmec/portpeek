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

	"github.com/kaanemec/portpeek/internal/inspect"
)

// Exit codes returned by Run.
const (
	exitOK       = 0 // at least one owner found
	exitNoMatch  = 1 // no matching socket
	exitBadInput = 2 // invalid input or usage
	exitFailure  = 3 // inspection failed

	exitInterrupted = 130 // interrupted; shell convention of 128 + SIGINT
)

// Version is the portpeek version, overridden at build time with -ldflags.
var Version = "dev"

const usageHint = "Try 'portpeek --help' for usage."

const helpText = `Usage: portpeek <port> [--tcp|--udp] [--json]

Show which process is using a local port.

Options:
  --tcp       only look at TCP sockets
  --udp       only look at UDP sockets
  --json      print machine-readable JSON (schema 1)
  --version   print the version and exit
  -h, --help  show this help and exit

Exit codes:
  0  at least one process uses the port
  1  no listening or bound socket on the port
  2  invalid input
  3  inspection failed (tool missing, permission denied, command error)
130  interrupted
`

// options holds the parsed command line.
type options struct {
	port     int
	protocol inspect.Protocol
	json     bool
	version  bool
	help     bool
}

// Run parses args (without the program name), runs the inspector, writes the
// output, and returns the process exit code.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer, ins inspect.Inspector) int {
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
		_, _ = fmt.Fprintln(stderr, "portpeek: interrupted")
		return exitInterrupted
	}
	if err != nil {
		return reportFailure(stdout, stderr, q, err, opts.json)
	}

	if err := render(stdout, q, res.Owners, opts.json); err != nil {
		_, _ = fmt.Fprintf(stderr, "portpeek: writing output: %v\n", err)
		return exitFailure
	}
	if len(res.Owners) == 0 {
		return exitNoMatch
	}
	return exitOK
}

// parseArgs parses flags and the single positional port. Flags may appear
// before or after the port, which the standard flag package does not allow on
// its own, so flag parsing is repeated after each positional argument.
func parseArgs(args []string) (options, error) {
	var opts options
	var tcp, udp bool

	fs := flag.NewFlagSet("portpeek", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&tcp, "tcp", false, "")
	fs.BoolVar(&udp, "udp", false, "")
	fs.BoolVar(&opts.json, "json", false, "")
	fs.BoolVar(&opts.version, "version", false, "")

	// Everything after a literal "--" is positional.
	var positional []string
	if i := slices.Index(args, "--"); i >= 0 {
		positional = append(positional, args[i+1:]...)
		args = args[:i]
	}

	var leading []string
	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return options{help: true}, nil
			}
			return options{}, err
		}
		rest = fs.Args()
		if len(rest) == 0 {
			break
		}
		leading = append(leading, rest[0])
		rest = rest[1:]
	}
	positional = append(leading, positional...)

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

	port, err := parsePort(positional)
	if err != nil {
		return options{}, err
	}
	opts.port = port
	return opts, nil
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

// describeError maps an inspection error to a stable kind string and a
// one or two line friendly message. The slice always has at least one line.
func describeError(q inspect.Query, err error) (string, []string) {
	var ie *inspect.Error
	if !errors.As(err, &ie) {
		return "unknown", []string{err.Error()}
	}

	switch ie.Kind {
	case inspect.KindToolMissing:
		return string(ie.Kind), []string{
			fmt.Sprintf("required tool %q is not installed, so port %d cannot be inspected.", ie.Op, q.Port),
			"Install it and make sure it is on your PATH.",
		}
	case inspect.KindPermissionDenied:
		return string(ie.Kind), []string{
			fmt.Sprintf("permission denied while inspecting port %d.", q.Port),
			fmt.Sprintf("Re-run with sudo for full details: sudo portpeek %d", q.Port),
		}
	default:
		return string(ie.Kind), []string{err.Error()}
	}
}
