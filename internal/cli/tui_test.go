package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/kaanemec/portpeek/internal/inspect"
)

// fakeLister satisfies inspect.Lister; the tui tests only pass it through.
type fakeLister struct{}

func (fakeLister) List(context.Context) (inspect.Snapshot, error) { return inspect.Snapshot{}, nil }

// fakeTUI records how Run invoked the terminal interface.
type fakeTUI struct {
	err   error
	calls int
	opts  TUIOptions
}

func (f *fakeTUI) run(_ context.Context, opts TUIOptions) error {
	f.calls++
	f.opts = opts
	return f.err
}

func TestRun_TUI(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		args         []string
		tuiErr       error
		wantCode     int
		wantErr      string
		wantCalls    int
		wantInterval time.Duration
	}{
		{
			name:         "default interval",
			args:         []string{"tui"},
			wantCode:     exitOK,
			wantCalls:    1,
			wantInterval: 5 * time.Second,
		},
		{
			name:         "interval",
			args:         []string{"tui", "--interval", "10s"},
			wantCode:     exitOK,
			wantCalls:    1,
			wantInterval: 10 * time.Second,
		},
		{
			name:         "minimum interval",
			args:         []string{"tui", "--interval=1s"},
			wantCode:     exitOK,
			wantCalls:    1,
			wantInterval: time.Second,
		},
		{
			name:     "interval below minimum",
			args:     []string{"tui", "--interval", "500ms"},
			wantCode: exitBadInput,
			wantErr:  "portpeek: --interval must be at least 1s, got 500ms\n" + usageHint + "\n",
		},
		{
			name:     "interval not a duration",
			args:     []string{"tui", "--interval", "soon"},
			wantCode: exitBadInput,
			wantErr:  "portpeek: invalid value \"soon\" for flag -interval: parse error\n" + usageHint + "\n",
		},
		{
			name:     "json",
			args:     []string{"tui", "--json"},
			wantCode: exitBadInput,
			wantErr:  "portpeek: --json cannot be used with tui\n" + usageHint + "\n",
		},
		{
			name:     "port",
			args:     []string{"tui", "3000"},
			wantCode: exitBadInput,
			wantErr:  "portpeek: tui shows every port and takes no arguments, got \"3000\"\n" + usageHint + "\n",
		},
		{
			name:     "port before tui",
			args:     []string{"3000", "tui"},
			wantCode: exitBadInput,
			wantErr:  "portpeek: expected one port, got 2 arguments\n" + usageHint + "\n",
		},
		{
			name:     "one-port flag",
			args:     []string{"tui", "--stop"},
			wantCode: exitBadInput,
			wantErr:  "portpeek: flag provided but not defined: -stop\n" + usageHint + "\n",
		},
		{
			name:      "interrupted",
			args:      []string{"tui"},
			tuiErr:    fmt.Errorf("terminal interface: %w", context.Canceled),
			wantCode:  exitInterrupted,
			wantErr:   "portpeek: interrupted\n",
			wantCalls: 1,
		},
		{
			name:      "not a terminal",
			args:      []string{"tui"},
			tuiErr:    ErrNotTerminal,
			wantCode:  exitBadInput,
			wantErr:   "portpeek: tui needs an interactive terminal\n",
			wantCalls: 1,
		},
		{
			name:      "interface failure",
			args:      []string{"tui"},
			tuiErr:    errors.New("boom"),
			wantCode:  exitFailure,
			wantErr:   "portpeek: terminal interface failed: boom\n",
			wantCalls: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ins := &fakeInspector{}
			ui := &fakeTUI{err: tt.tuiErr}
			deps := Deps{Inspector: ins, Lister: fakeLister{}, TUI: ui.run}
			var stdout, stderr bytes.Buffer

			code := Run(t.Context(), tt.args, &stdout, &stderr, deps)

			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d", code, tt.wantCode)
			}
			if got := stderr.String(); got != tt.wantErr {
				t.Errorf("stderr = %q, want %q", got, tt.wantErr)
			}
			if stdout.Len() != 0 {
				t.Errorf("stdout = %q, want empty", stdout.String())
			}
			if ui.calls != tt.wantCalls {
				t.Fatalf("TUI calls = %d, want %d", ui.calls, tt.wantCalls)
			}
			if ins.called {
				t.Error("inspector was called by the tui subcommand itself")
			}
			if tt.wantCalls == 0 {
				return
			}
			if ui.opts.Interval != tt.wantInterval && tt.wantInterval != 0 {
				t.Errorf("interval = %s, want %s", ui.opts.Interval, tt.wantInterval)
			}
			if ui.opts.Inspector != ins || ui.opts.Lister != (fakeLister{}) {
				t.Errorf("TUI got inspector %v and lister %v, want the deps", ui.opts.Inspector, ui.opts.Lister)
			}
		})
	}
}

func TestRun_TUIHelp(t *testing.T) {
	t.Parallel()

	ui := &fakeTUI{}
	var stdout, stderr bytes.Buffer
	code := Run(t.Context(), []string{"tui", "--help"}, &stdout, &stderr, Deps{TUI: ui.run})

	if code != exitOK || stdout.String() != helpText || ui.calls != 0 {
		t.Errorf("got code %d, %d TUI calls, stdout %q; want help text and no TUI", code, ui.calls, stdout.String())
	}
}

func TestRun_TUIUnavailable(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	code := Run(t.Context(), []string{"tui"}, &stdout, &stderr, Deps{Inspector: &fakeInspector{}})

	want := "portpeek: the terminal interface is not available in this build\n"
	if code != exitFailure || stderr.String() != want {
		t.Errorf("got code %d, stderr %q; want %d, %q", code, stderr.String(), exitFailure, want)
	}
}

func TestErrorText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		q    inspect.Query
		err  error
		want string
	}{
		{
			name: "tool missing for one port",
			q:    inspect.Query{Port: 3000},
			err:  &inspect.Error{Kind: inspect.KindToolMissing, Op: "lsof"},
			want: `required tool "lsof" is not installed, so port 3000 cannot be inspected. ` +
				"Install it and make sure it is on your PATH.",
		},
		{
			name: "tool missing for the listing",
			err:  &inspect.Error{Kind: inspect.KindToolMissing, Op: "ss"},
			want: `required tool "ss" is not installed, so local ports cannot be listed. ` +
				"Install it (part of the iproute2 package) and make sure it is on your PATH.",
		},
		{
			name: "permission denied for the listing",
			err:  &inspect.Error{Kind: inspect.KindPermissionDenied, Op: "lsof"},
			want: "permission denied while listing local ports. Re-run with sudo for full details: sudo portpeek tui",
		},
		{
			name: "other error",
			err:  &inspect.Error{Kind: inspect.KindCommandFailed, Op: "lsof", Err: errors.New("exit status 2")},
			want: "lsof: command-failed: exit status 2",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := ErrorText(tt.q, tt.err); got != tt.want {
				t.Errorf("ErrorText = %q, want %q", got, tt.want)
			}
		})
	}
}
