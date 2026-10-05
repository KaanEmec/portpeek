// Command portpeek reports which local process is using a port.
package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/kaanemec/portpeek/internal/cli"
	"github.com/kaanemec/portpeek/internal/inspect"
	"github.com/kaanemec/portpeek/internal/tui"
)

// platformInspector is what every platform adapter provides: one-port
// inspection and the cheap listing of every port.
type platformInspector interface {
	inspect.Inspector
	inspect.Lister
}

func main() {
	os.Exit(run())
}

func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	ins := defaultInspector()
	deps := cli.Deps{Inspector: ins, Lister: ins, TUI: tui.Run}
	return cli.Run(ctx, os.Args[1:], os.Stdout, os.Stderr, deps)
}
