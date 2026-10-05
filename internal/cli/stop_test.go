package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kaanemec/portpeek/internal/inspect"
)

// fakeStopper records the side effects the stop flow asks for.
type fakeStopper struct {
	terminal  bool
	stdin     io.Reader
	signalErr error
	// aliveChecks is how many liveness checks report the process alive
	// before it is gone; -1 means it never exits.
	aliveChecks int

	termAsked bool
	signalled []int
	checks    int
	slept     time.Duration
}

func (f *fakeStopper) stopper() stopper {
	stdin := f.stdin
	if stdin == nil {
		stdin = strings.NewReader("")
	}
	return stopper{
		stdin: stdin,
		isTerminal: func() bool {
			f.termAsked = true
			return f.terminal
		},
		terminate: func(pid int) error {
			if f.signalErr != nil {
				return f.signalErr
			}
			f.signalled = append(f.signalled, pid)
			return nil
		},
		alive: func(int) bool {
			f.checks++
			return f.aliveChecks < 0 || f.checks <= f.aliveChecks
		},
		sleep: func(d time.Duration) { f.slept += d },
	}
}

func pythonOwner() inspect.Owner {
	return inspect.Owner{
		Process: inspect.Process{
			PID:         500,
			Name:        "python3",
			User:        "root",
			Command:     "python3 -m http.server 3000",
			WorkingDir:  "/srv",
			Unavailable: map[inspect.Field]string{},
		},
		Sockets: []inspect.Socket{
			{Protocol: inspect.TCP, Family: inspect.IPv6, Address: "*", Port: 3000, State: inspect.StateListen},
		},
	}
}

// renderedText is the normal text result that --stop prints before acting.
func renderedText(t *testing.T, owners []inspect.Owner) string {
	t.Helper()
	var b bytes.Buffer
	if err := render(&b, inspect.Query{Port: 3000}, owners, false); err != nil {
		t.Fatalf("render: %v", err)
	}
	return b.String()
}

func TestRun_Stop(t *testing.T) {
	setPrivileged(t, true)

	node := []inspect.Owner{nodeOwner()}
	both := []inspect.Owner{nodeOwner(), pythonOwner()}
	renamed := nodeOwner()
	renamed.Process.Name = "ruby"
	reused := nodeOwner()
	reused.Process.PID = 48999
	unknown := []inspect.Owner{unknownOwner()}
	nodeAndUnknown := []inspect.Owner{nodeOwner(), unknownOwner()}
	twoUnknown := []inspect.Owner{unknownOwner(), unknownOwner()}

	const unknownRefused = "portpeek: owner of port 3000 is unknown; nothing was stopped\n"

	const prompt = "Send SIGTERM to node (PID 48213)? [y/N] "
	const exited = "Sent SIGTERM to node (PID 48213); process exited.\n"

	tests := []struct {
		name string
		args []string
		// results is the first inspection followed by the recheck.
		results     []inspect.Result
		terminal    bool
		stdin       string
		signalErr   error
		aliveChecks int
		wantCode    int
		// wantOut is what follows the normal text result on stdout.
		wantOut     string
		wantErr     string
		wantSignal  []int
		wantChecks  int
		wantInspect int
	}{
		{
			name:        "confirmed yes",
			args:        []string{"3000", "--stop"},
			results:     []inspect.Result{{Owners: node}},
			terminal:    true,
			stdin:       "y\n",
			wantCode:    exitOK,
			wantOut:     exited,
			wantErr:     prompt,
			wantSignal:  []int{48213},
			wantChecks:  1,
			wantInspect: 2,
		},
		{
			name:        "confirmed YES without trailing newline",
			args:        []string{"--stop", "3000"},
			results:     []inspect.Result{{Owners: node}},
			terminal:    true,
			stdin:       " YES ",
			wantCode:    exitOK,
			wantOut:     exited,
			wantErr:     prompt,
			wantSignal:  []int{48213},
			wantChecks:  1,
			wantInspect: 2,
		},
		{
			name:        "declined",
			args:        []string{"3000", "--stop"},
			results:     []inspect.Result{{Owners: node}},
			terminal:    true,
			stdin:       "n\n",
			wantCode:    exitNotStopped,
			wantErr:     prompt + "Not stopped.\n",
			wantInspect: 1,
		},
		{
			name:        "empty answer declines",
			args:        []string{"3000", "--stop"},
			results:     []inspect.Result{{Owners: node}},
			terminal:    true,
			stdin:       "\n",
			wantCode:    exitNotStopped,
			wantErr:     prompt + "Not stopped.\n",
			wantInspect: 1,
		},
		{
			name:        "closed stdin declines",
			args:        []string{"3000", "--stop"},
			results:     []inspect.Result{{Owners: node}},
			terminal:    true,
			wantCode:    exitNotStopped,
			wantErr:     prompt + "Not stopped.\n",
			wantInspect: 1,
		},
		{
			name:        "not a terminal without force",
			args:        []string{"3000", "--stop"},
			results:     []inspect.Result{{Owners: node}},
			stdin:       "y\n",
			wantCode:    exitNotStopped,
			wantErr:     "portpeek: refusing to stop without confirmation; re-run with --force in non-interactive use\n",
			wantInspect: 1,
		},
		{
			name:        "not a terminal with force",
			args:        []string{"3000", "--stop", "--force"},
			results:     []inspect.Result{{Owners: node}},
			wantCode:    exitOK,
			wantOut:     exited,
			wantSignal:  []int{48213},
			wantChecks:  1,
			wantInspect: 2,
		},
		{
			name:        "several owners without pid",
			args:        []string{"3000", "--stop", "--force"},
			results:     []inspect.Result{{Owners: both}},
			wantCode:    exitNotStopped,
			wantInspect: 1,
			wantErr: "portpeek: port 3000 is used by several processes; choose one with --pid <pid>:\n" +
				"  48213    node\n" +
				"  500      python3\n",
		},
		{
			name:        "pid selects one of several owners",
			args:        []string{"3000", "--stop", "--force", "--pid", "500"},
			results:     []inspect.Result{{Owners: both}},
			wantCode:    exitOK,
			wantOut:     "Sent SIGTERM to python3 (PID 500); process exited.\n",
			wantSignal:  []int{500},
			wantChecks:  1,
			wantInspect: 2,
		},
		{
			name:        "pid with equals sign prompts for that owner",
			args:        []string{"3000", "--stop", "--pid=500"},
			results:     []inspect.Result{{Owners: both}},
			terminal:    true,
			stdin:       "yes\n",
			wantCode:    exitOK,
			wantOut:     "Sent SIGTERM to python3 (PID 500); process exited.\n",
			wantErr:     "Send SIGTERM to python3 (PID 500)? [y/N] ",
			wantSignal:  []int{500},
			wantChecks:  1,
			wantInspect: 2,
		},
		{
			name:        "unknown pid",
			args:        []string{"3000", "--stop", "--force", "--pid", "999"},
			results:     []inspect.Result{{Owners: both}},
			wantCode:    exitNotStopped,
			wantErr:     "portpeek: PID 999 does not use port 3000; nothing was stopped\n",
			wantInspect: 1,
		},
		{
			name:        "unknown owner is refused with force",
			args:        []string{"3000", "--stop", "--force"},
			results:     []inspect.Result{{Owners: unknown}},
			wantCode:    exitNotStopped,
			wantErr:     unknownRefused,
			wantInspect: 1,
		},
		{
			name:        "unknown owner is refused before prompting",
			args:        []string{"3000", "--stop"},
			results:     []inspect.Result{{Owners: unknown}},
			terminal:    true,
			stdin:       "y\n",
			wantCode:    exitNotStopped,
			wantErr:     unknownRefused,
			wantInspect: 1,
		},
		{
			name:        "several unknown owners are refused",
			args:        []string{"3000", "--stop", "--force"},
			results:     []inspect.Result{{Owners: twoUnknown}},
			wantCode:    exitNotStopped,
			wantErr:     unknownRefused,
			wantInspect: 1,
		},
		{
			name:        "pid does not match an unknown owner",
			args:        []string{"3000", "--stop", "--force", "--pid", "500"},
			results:     []inspect.Result{{Owners: unknown}},
			wantCode:    exitNotStopped,
			wantErr:     "portpeek: PID 500 does not use port 3000; nothing was stopped\n",
			wantInspect: 1,
		},
		{
			name:        "several owners list an unknown owner without a pid",
			args:        []string{"3000", "--stop", "--force"},
			results:     []inspect.Result{{Owners: nodeAndUnknown}},
			wantCode:    exitNotStopped,
			wantInspect: 1,
			wantErr: "portpeek: port 3000 is used by several processes; choose one with --pid <pid>:\n" +
				"  48213    node\n" +
				"  unknown  unavailable (not readable without elevated privileges)\n",
		},
		{
			name:        "pid selects the known owner next to an unknown one",
			args:        []string{"3000", "--stop", "--force", "--pid", "48213"},
			results:     []inspect.Result{{Owners: nodeAndUnknown}},
			wantCode:    exitOK,
			wantOut:     exited,
			wantSignal:  []int{48213},
			wantChecks:  1,
			wantInspect: 2,
		},
		{
			name:        "recheck finds no owner",
			args:        []string{"3000", "--stop", "--force"},
			results:     []inspect.Result{{Owners: node}, {}},
			wantCode:    exitNotStopped,
			wantErr:     "portpeek: process changed since inspection; nothing was stopped\n",
			wantInspect: 2,
		},
		{
			name:        "recheck finds another pid",
			args:        []string{"3000", "--stop"},
			results:     []inspect.Result{{Owners: node}, {Owners: []inspect.Owner{reused}}},
			terminal:    true,
			stdin:       "y\n",
			wantCode:    exitNotStopped,
			wantErr:     prompt + "portpeek: process changed since inspection; nothing was stopped\n",
			wantInspect: 2,
		},
		{
			name:        "recheck finds a different name",
			args:        []string{"3000", "--stop", "--force"},
			results:     []inspect.Result{{Owners: node}, {Owners: []inspect.Owner{renamed}}},
			wantCode:    exitNotStopped,
			wantErr:     "portpeek: process changed since inspection; nothing was stopped\n",
			wantInspect: 2,
		},
		{
			name:        "signal refused",
			args:        []string{"3000", "--stop", "--force"},
			results:     []inspect.Result{{Owners: node}},
			signalErr:   syscall.EPERM,
			wantCode:    exitNotStopped,
			wantErr:     "portpeek: could not signal PID 48213: operation not permitted\n",
			wantInspect: 2,
		},
		{
			name:        "exits after a few polls",
			args:        []string{"3000", "--stop", "--force"},
			results:     []inspect.Result{{Owners: node}},
			aliveChecks: 3,
			wantCode:    exitOK,
			wantOut:     exited,
			wantSignal:  []int{48213},
			wantChecks:  4,
			wantInspect: 2,
		},
		{
			name:        "still running after timeout",
			args:        []string{"3000", "--stop", "--force"},
			results:     []inspect.Result{{Owners: node}},
			aliveChecks: -1,
			wantCode:    exitOK,
			wantOut:     "Sent SIGTERM to node (PID 48213); still running after 2s. To force: kill -9 48213\n",
			wantSignal:  []int{48213},
			wantChecks:  21,
			wantInspect: 2,
		},
		{
			name:        "no owner",
			args:        []string{"3000", "--stop", "--force"},
			results:     []inspect.Result{{}},
			wantCode:    exitNoMatch,
			wantInspect: 1,
		},
		{
			name:        "plain inspection sends nothing",
			args:        []string{"3000"},
			results:     []inspect.Result{{Owners: node}},
			terminal:    true,
			stdin:       "y\n",
			wantCode:    exitOK,
			wantInspect: 1,
		},
		{
			name:     "stop with json",
			args:     []string{"3000", "--stop", "--json"},
			wantCode: exitBadInput,
			wantErr:  "portpeek: --stop cannot be used with --json\n" + usageHint + "\n",
		},
		{
			name:     "force without stop",
			args:     []string{"3000", "--force"},
			wantCode: exitBadInput,
			wantErr:  "portpeek: --force requires --stop\n" + usageHint + "\n",
		},
		{
			name:     "pid without stop",
			args:     []string{"3000", "--pid", "500"},
			wantCode: exitBadInput,
			wantErr:  "portpeek: --pid requires --stop\n" + usageHint + "\n",
		},
		{
			name:     "pid not a number",
			args:     []string{"3000", "--stop", "--pid", "abc"},
			wantCode: exitBadInput,
			wantErr: "portpeek: invalid value \"abc\" for flag -pid: must be a positive process ID\n" +
				usageHint + "\n",
		},
		{
			name:     "pid zero",
			args:     []string{"3000", "--stop", "--pid", "0"},
			wantCode: exitBadInput,
			wantErr: "portpeek: invalid value \"0\" for flag -pid: must be a positive process ID\n" +
				usageHint + "\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fake := &fakeInspector{results: tt.results}
			sys := &fakeStopper{
				terminal:    tt.terminal,
				stdin:       strings.NewReader(tt.stdin),
				signalErr:   tt.signalErr,
				aliveChecks: tt.aliveChecks,
			}
			var stdout, stderr bytes.Buffer

			code := run(t.Context(), tt.args, stdio{stdout: &stdout, stderr: &stderr}, fake, sys.stopper())

			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d", code, tt.wantCode)
			}
			wantOut := tt.wantOut
			if len(tt.results) > 0 {
				wantOut = renderedText(t, tt.results[0].Owners) + tt.wantOut
			}
			if got := stdout.String(); got != wantOut {
				t.Errorf("stdout mismatch\n got:\n%s\nwant:\n%s", got, wantOut)
			}
			if got := stderr.String(); got != tt.wantErr {
				t.Errorf("stderr mismatch\n got:\n%q\nwant:\n%q", got, tt.wantErr)
			}
			if !slices.Equal(sys.signalled, tt.wantSignal) {
				t.Errorf("signalled PIDs = %v, want %v", sys.signalled, tt.wantSignal)
			}
			if sys.checks != tt.wantChecks {
				t.Errorf("liveness checks = %d, want %d", sys.checks, tt.wantChecks)
			}
			if want := time.Duration(max(tt.wantChecks-1, 0)) * stopPollInterval; sys.slept != want {
				t.Errorf("slept %s, want %s", sys.slept, want)
			}
			if fake.calls != tt.wantInspect {
				t.Errorf("inspections = %d, want %d", fake.calls, tt.wantInspect)
			}
			if !slices.Contains(tt.args, "--stop") && sys.termAsked {
				t.Error("plain inspection consulted the terminal")
			}
		})
	}
}

// failingRecheck answers the first inspection and fails every later one.
type failingRecheck struct {
	first inspect.Result
	err   error
	calls int
}

func (f *failingRecheck) Inspect(context.Context, inspect.Query) (inspect.Result, error) {
	f.calls++
	if f.calls == 1 {
		return f.first, nil
	}
	return inspect.Result{}, f.err
}

func TestRun_StopRecheckError(t *testing.T) {
	setPrivileged(t, true)

	tests := []struct {
		name     string
		err      error
		wantCode int
		wantErr  string
	}{
		{
			name:     "inspection failure",
			err:      &inspect.Error{Kind: inspect.KindCommandFailed, Op: "lsof", Err: errors.New("exit status 2")},
			wantCode: exitNotStopped,
			wantErr:  "portpeek: could not recheck port 3000: lsof: command-failed: exit status 2; nothing was stopped\n",
		},
		{
			name:     "interrupted",
			err:      context.Canceled,
			wantCode: exitInterrupted,
			wantErr:  "portpeek: interrupted\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ins := &failingRecheck{first: inspect.Result{Owners: []inspect.Owner{nodeOwner()}}, err: tt.err}
			sys := &fakeStopper{}
			var stdout, stderr bytes.Buffer

			code := run(
				t.Context(),
				[]string{"3000", "--stop", "--force"},
				stdio{stdout: &stdout, stderr: &stderr},
				ins,
				sys.stopper(),
			)

			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d", code, tt.wantCode)
			}
			if got := stderr.String(); got != tt.wantErr {
				t.Errorf("stderr = %q, want %q", got, tt.wantErr)
			}
			if len(sys.signalled) != 0 {
				t.Errorf("signalled %v, want none", sys.signalled)
			}
		})
	}
}

func TestRun_StopInterruptedAtPrompt(t *testing.T) {
	setPrivileged(t, true)

	// A pipe that is never written blocks the prompt until ctx ends.
	stdinR, stdinW := io.Pipe()
	t.Cleanup(func() { _ = stdinW.Close() })
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	fake := &fakeInspector{result: inspect.Result{Owners: []inspect.Owner{nodeOwner()}}}
	sys := &fakeStopper{terminal: true, stdin: stdinR}
	var stdout, stderr bytes.Buffer

	code := run(ctx, []string{"3000", "--stop"}, stdio{stdout: &stdout, stderr: &stderr}, fake, sys.stopper())

	if code != exitInterrupted {
		t.Errorf("exit code = %d, want %d", code, exitInterrupted)
	}
	wantErr := "Send SIGTERM to node (PID 48213)? [y/N] \nportpeek: interrupted\n"
	if got := stderr.String(); got != wantErr {
		t.Errorf("stderr = %q, want %q", got, wantErr)
	}
	if len(sys.signalled) != 0 || fake.calls != 1 {
		t.Errorf("signalled %v after %d inspections, want no signal and 1 inspection", sys.signalled, fake.calls)
	}
}
