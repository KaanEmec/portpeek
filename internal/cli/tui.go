package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"golang.org/x/term"

	"github.com/kaanemec/portpeek/internal/inspect"
)

// tuiCommand is the subcommand that opens the terminal interface, the same
// overview as plain portpeek.
const tuiCommand = "tui"

// intervalFlag is the one flag of the overview.
const intervalFlag = "interval"

// Auto-refresh period of the terminal interface.
const (
	defaultTUIInterval = 5 * time.Second
	minTUIInterval     = time.Second
)

// TUIOptions is what the tui subcommand hands to Deps.TUI.
type TUIOptions struct {
	Inspector inspect.Inspector
	Lister    inspect.Lister
	// Interval is the auto-refresh period, at least one second.
	Interval time.Duration
}

// ErrNotTerminal is returned by Deps.TUI when the terminal interface cannot
// run because output is not an interactive terminal.
var ErrNotTerminal = errors.New("tui needs an interactive terminal")

// isOverview reports whether args, given without the tui subcommand, ask for
// the overview: no positional port and neither --help nor --version. A flag
// of the one-port form without a port is an error naming the flag. Arguments
// the one-port parser rejects are left to it, so it reports the problem.
func isOverview(args []string) (bool, error) {
	var opts options
	var tcp, udp bool
	fs := newPortFlagSet(&opts, &tcp, &udp)
	fs.String(intervalFlag, "", "")

	positional, err := parseInterspersed(fs, args)
	if err != nil || len(positional) > 0 || opts.version {
		return false, nil
	}

	var portOnly string
	fs.Visit(func(f *flag.Flag) {
		if f.Name != intervalFlag && portOnly == "" {
			portOnly = f.Name
		}
	})
	if portOnly != "" {
		return false, fmt.Errorf("--%s requires a port", portOnly)
	}
	return true, nil
}

// runOverview opens the overview for plain portpeek. Without an interactive
// terminal, such as in a script or a pipe, the user most likely forgot the
// port, so it prints usage instead of starting the interface.
func runOverview(ctx context.Context, args []string, out stdio, deps Deps) int {
	interactive := deps.interactive
	if interactive == nil {
		interactive = interactiveTerminal
	}
	if !interactive() {
		_, _ = fmt.Fprintf(out.stderr,
			"portpeek: no port given and no interactive terminal; usage: portpeek <port>\n%s\n", usageHint)
		return exitBadInput
	}
	return runTUI(ctx, args, out, deps)
}

// interactiveTerminal reports whether stdin and stdout are both interactive
// terminals, as the interface needs to read keys and draw.
func interactiveTerminal() bool {
	return stdinIsTerminal() && term.IsTerminal(int(os.Stdout.Fd()))
}

// runTUI parses the tui subcommand's arguments and runs the interface. A
// Deps.TUI error wrapping context.Canceled means the user interrupted it.
func runTUI(ctx context.Context, args []string, out stdio, deps Deps) int {
	interval, help, err := parseTUIArgs(args)
	if err != nil {
		_, _ = fmt.Fprintf(out.stderr, "portpeek: %v\n%s\n", err, usageHint)
		return exitBadInput
	}
	if help {
		_, _ = fmt.Fprint(out.stdout, helpText)
		return exitOK
	}
	if deps.TUI == nil || deps.Lister == nil || deps.Inspector == nil {
		_, _ = fmt.Fprintln(out.stderr, "portpeek: the terminal interface is not available in this build")
		return exitFailure
	}

	err = deps.TUI(ctx, TUIOptions{Inspector: deps.Inspector, Lister: deps.Lister, Interval: interval})
	switch {
	case err == nil:
		return exitOK
	case errors.Is(err, context.Canceled):
		return reportInterrupted(out.stderr)
	case errors.Is(err, ErrNotTerminal):
		_, _ = fmt.Fprintf(out.stderr, "portpeek: %v\n", err)
		return exitBadInput
	default:
		_, _ = fmt.Fprintf(out.stderr, "portpeek: terminal interface failed: %v\n", err)
		return exitFailure
	}
}

// parseTUIArgs parses the arguments after "tui". The interface covers every
// port and has no JSON form, so a port or --json is a usage error.
func parseTUIArgs(args []string) (interval time.Duration, help bool, err error) {
	var asJSON bool
	fs := flag.NewFlagSet("portpeek tui", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.DurationVar(&interval, intervalFlag, defaultTUIInterval, "")
	fs.BoolVar(&asJSON, "json", false, "")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0, true, nil
		}
		return 0, false, err
	}
	switch {
	case asJSON:
		return 0, false, errors.New("--json cannot be used with tui")
	case fs.NArg() > 0:
		return 0, false, fmt.Errorf("tui shows every port and takes no arguments, got %q", fs.Arg(0))
	case interval < minTUIInterval:
		return 0, false, fmt.Errorf("--interval must be at least %s, got %s", minTUIInterval, interval)
	}
	return interval, false, nil
}
