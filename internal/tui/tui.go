// Package tui implements `portpeek tui`: a searchable, refreshing overview of
// local ports built on Bubble Tea. It lists sockets through inspect.Lister,
// explains one port through inspect.Inspector with the CLI's own text
// renderer, and stops a process only through the CLI's verified stop core.
package tui

import (
	"context"
	"errors"
	"os"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"golang.org/x/term"

	"github.com/kaanemec/portpeek/internal/cli"
	"github.com/kaanemec/portpeek/internal/inspect"
)

// Run shows the interface on the terminal until the user quits. It returns
// cli.ErrNotTerminal when stdout is not a terminal, and an error wrapping
// context.Canceled when the user presses Ctrl-C or ctx ends.
func Run(ctx context.Context, opts cli.TUIOptions) error {
	if !term.IsTerminal(int(os.Stdout.Fd())) {
		return cli.ErrNotTerminal
	}

	m := newModel(ctx, config{
		lister:   opts.Lister,
		ins:      opts.Inspector,
		interval: opts.Interval,
		stop: func(ctx context.Context, q inspect.Query, target inspect.Process) cli.StopResult {
			return cli.StopVerified(ctx, opts.Inspector, q, target)
		},
		now:   time.Now,
		after: after,
	})
	_, err := tea.NewProgram(m, tea.WithContext(ctx)).Run()
	if errors.Is(err, tea.ErrInterrupted) || errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	return err
}

// after delivers msg once d has passed.
func after(d time.Duration, msg tea.Msg) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return msg })
}

// keyMap is every key binding. The help text of each binding is what the
// footer shows.
type keyMap struct {
	up, down, pageUp, pageDown, top, bottom key.Binding

	details, search, clear, sort, refresh, pause, quit, interrupt key.Binding

	stop, yes, back key.Binding
}

func defaultKeyMap() keyMap {
	return keyMap{
		up:       key.NewBinding(key.WithKeys("up"), key.WithHelp("↑/↓", "move")),
		down:     key.NewBinding(key.WithKeys("down")),
		pageUp:   key.NewBinding(key.WithKeys("pgup")),
		pageDown: key.NewBinding(key.WithKeys("pgdown")),
		top:      key.NewBinding(key.WithKeys("home")),
		bottom:   key.NewBinding(key.WithKeys("end")),

		details:   key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "details")),
		search:    key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "search")),
		clear:     key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "clear")),
		sort:      key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "sort")),
		refresh:   key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
		pause:     key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "pause")),
		quit:      key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
		interrupt: key.NewBinding(key.WithKeys("ctrl+c")),

		// The stop key is only bound in details; the table never changes
		// anything.
		stop: key.NewBinding(key.WithKeys("k"), key.WithHelp("k", "stop")),
		yes:  key.NewBinding(key.WithKeys("y", "Y")),
		back: key.NewBinding(key.WithKeys("esc"), key.WithHelp("Esc", "back")),
	}
}
