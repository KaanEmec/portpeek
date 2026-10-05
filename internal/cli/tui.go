package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/kaanemec/portpeek/internal/inspect"
)

// tuiCommand is the subcommand that opens the terminal interface.
const tuiCommand = "tui"

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
	fs.DurationVar(&interval, "interval", defaultTUIInterval, "")
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
