package lsof

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/kaanemec/portpeek/internal/inspect"
)

type response struct {
	stdout string
	stderr string
	code   int
	err    error
}

// fakeRunner answers commands from a table keyed by "name arg1 arg2 ...".
// Unknown commands fail as if the command were not installed.
type fakeRunner map[string]response

func (f fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, []byte, int, error) {
	key := strings.Join(append([]string{name}, args...), " ")
	r, ok := f[key]
	if !ok {
		return nil, nil, -1, fmt.Errorf("unexpected command %q: %w", key, exec.ErrNotFound)
	}
	return []byte(r.stdout), []byte(r.stderr), r.code, r.err
}

func findKey(selector string) string { return "lsof -nP -F pcnLTtfPR0 -i " + selector }
func psKey(pid string) string        { return "ps -o command= -p " + pid }
func cwdKey(pid string) string       { return "lsof -nP -F n -d cwd -a -p " + pid }

func TestInspector_Inspect(t *testing.T) {
	t.Parallel()

	runner := fakeRunner{
		findKey(":48123"): {stdout: string(readFixture(t, "established_mixed.txt"))},
		psKey("42148"):    {stdout: "python3 -m http.server 48123\n"},
		cwdKey("42148"):   {stdout: "p42148\nfcwd\nn/Users/me/site\n"},
	}
	got, err := NewWithRunner(runner).Inspect(context.Background(), inspect.Query{Port: 48123})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if len(got.Owners) != 1 {
		t.Fatalf("Inspect owners = %+v, want 1", got.Owners)
	}
	p := got.Owners[0].Process
	if p.PID != 42148 || p.Name != "Python" || p.User != "kaanemec" {
		t.Errorf("process identity = %+v", p)
	}
	if p.Command != "python3 -m http.server 48123" {
		t.Errorf("Command = %q", p.Command)
	}
	if p.WorkingDir != "/Users/me/site" {
		t.Errorf("WorkingDir = %q", p.WorkingDir)
	}
	if len(p.Unavailable) != 0 {
		t.Errorf("Unavailable = %v, want empty", p.Unavailable)
	}
	if got.Query.Port != 48123 {
		t.Errorf("Result.Query = %+v", got.Query)
	}
}

func TestInspector_Inspect_ProtocolNarrowing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		protocol inspect.Protocol
		selector string
	}{
		{name: "both", protocol: "", selector: ":48125"},
		{name: "tcp", protocol: inspect.TCP, selector: "tcp:48125"},
		{name: "udp", protocol: inspect.UDP, selector: "udp:48125"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runner := fakeRunner{findKey(tt.selector): {code: 1}}
			q := inspect.Query{Port: 48125, Protocol: tt.protocol}
			if _, err := NewWithRunner(runner).Inspect(context.Background(), q); err != nil {
				t.Errorf("Inspect(%+v): %v", q, err)
			}
		})
	}
}

func TestInspector_Inspect_NoMatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		resp response
		port int
	}{
		{name: "exit 1 with empty output", resp: response{code: 1}, port: 48123},
		{
			name: "only remote port matched",
			resp: response{stdout: string(readFixture(t, "remote_port_only.txt"))},
			port: 443,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runner := fakeRunner{findKey(fmt.Sprintf(":%d", tt.port)): tt.resp}
			got, err := NewWithRunner(runner).Inspect(context.Background(), inspect.Query{Port: tt.port})
			if err != nil {
				t.Fatalf("Inspect: %v", err)
			}
			if got.Owners == nil || len(got.Owners) != 0 {
				t.Errorf("Owners = %#v, want empty non-nil slice", got.Owners)
			}
		})
	}
}

func TestInspector_Inspect_Errors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		resp     response
		wantKind inspect.Kind
		wantText string
	}{
		{
			name:     "lsof not installed",
			resp:     response{code: -1, err: &exec.Error{Name: "lsof", Err: exec.ErrNotFound}},
			wantKind: inspect.KindToolMissing,
		},
		{
			name:     "permission denied on stderr",
			resp:     response{code: 1, stderr: "lsof: can't open /dev/kmem: Permission denied\n"},
			wantKind: inspect.KindPermissionDenied,
		},
		{
			name:     "operation not permitted on stderr",
			resp:     response{code: 1, stderr: "lsof: Operation not permitted\n"},
			wantKind: inspect.KindPermissionDenied,
		},
		{
			name:     "exit 1 with stderr",
			resp:     response{code: 1, stderr: "lsof: unsupported option\n"},
			wantKind: inspect.KindCommandFailed,
			wantText: "unsupported option",
		},
		{
			name:     "other exit code",
			resp:     response{code: 2},
			wantKind: inspect.KindCommandFailed,
			wantText: "exit status 2",
		},
		{
			name:     "unparseable output",
			resp:     response{stdout: "pnot-a-pid\x00\n"},
			wantKind: inspect.KindCommandFailed,
			wantText: "parsing output",
		},
		{
			name:     "command did not run",
			resp:     response{code: -1, err: errors.New("signal: killed")},
			wantKind: inspect.KindCommandFailed,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runner := fakeRunner{findKey(":3000"): tt.resp}
			_, err := NewWithRunner(runner).Inspect(context.Background(), inspect.Query{Port: 3000})
			if err == nil {
				t.Fatal("Inspect: expected error")
			}
			if got := inspect.KindOf(err); got != tt.wantKind {
				t.Errorf("KindOf(%v) = %q, want %q", err, got, tt.wantKind)
			}
			if !strings.Contains(err.Error(), tt.wantText) {
				t.Errorf("error %q does not contain %q", err, tt.wantText)
			}
		})
	}
}

func TestInspector_Inspect_InvalidQuery(t *testing.T) {
	t.Parallel()
	_, err := NewWithRunner(fakeRunner{}).Inspect(context.Background(), inspect.Query{Port: 0})
	if err == nil {
		t.Fatal("Inspect: expected error for port 0")
	}
}

func TestInspector_Inspect_EnrichmentFailures(t *testing.T) {
	t.Parallel()

	find := string(readFixture(t, "tcp4_listener.txt"))
	tests := []struct {
		name        string
		ps          response
		cwd         response
		wantCommand string
		wantDir     string
		wantReasons map[inspect.Field]string
	}{
		{
			name: "process exited before enrichment",
			ps:   response{code: 1},
			cwd:  response{code: 1},
			wantReasons: map[inspect.Field]string{
				inspect.FieldCommand:    reasonExited,
				inspect.FieldWorkingDir: reasonExited,
			},
		},
		{
			name:        "cwd hidden for another user's process",
			ps:          response{stdout: "/usr/sbin/sshd -D\n"},
			cwd:         response{code: 1},
			wantCommand: "/usr/sbin/sshd -D",
			wantReasons: map[inspect.Field]string{inspect.FieldWorkingDir: reasonPermission},
		},
		{
			name: "permission denied on stderr",
			ps:   response{code: 1, stderr: "ps: Operation not permitted\n"},
			cwd:  response{code: 1, stderr: "lsof: Permission denied\n"},
			wantReasons: map[inspect.Field]string{
				inspect.FieldCommand:    reasonPermission,
				inspect.FieldWorkingDir: reasonPermission,
			},
		},
		{
			name:        "tools fail to run",
			ps:          response{code: -1, err: errors.New("fork failed")},
			cwd:         response{code: -1, err: errors.New("fork failed")},
			wantReasons: map[inspect.Field]string{inspect.FieldCommand: reasonUnknown, inspect.FieldWorkingDir: reasonUnknown},
		},
		{
			name:        "cwd output without a cwd record",
			ps:          response{stdout: "python3 server.py\n"},
			cwd:         response{stdout: "p42148\n"},
			wantCommand: "python3 server.py",
			wantReasons: map[inspect.Field]string{inspect.FieldWorkingDir: reasonUnknown},
		},
		{
			name:        "unexpected exit codes",
			ps:          response{code: 2, stderr: "ps: weird\n"},
			cwd:         response{code: 2},
			wantReasons: map[inspect.Field]string{inspect.FieldCommand: reasonUnknown, inspect.FieldWorkingDir: reasonUnknown},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runner := fakeRunner{
				findKey(":48123"): {stdout: find},
				psKey("42148"):    tt.ps,
				cwdKey("42148"):   tt.cwd,
			}
			got, err := NewWithRunner(runner).Inspect(context.Background(), inspect.Query{Port: 48123})
			if err != nil {
				t.Fatalf("Inspect: %v (enrichment must not fail the inspection)", err)
			}
			if len(got.Owners) != 1 {
				t.Fatalf("Owners = %+v, want the owner kept despite enrichment failure", got.Owners)
			}
			p := got.Owners[0].Process
			if p.Command != tt.wantCommand || p.WorkingDir != tt.wantDir {
				t.Errorf("Command, WorkingDir = %q, %q; want %q, %q",
					p.Command, p.WorkingDir, tt.wantCommand, tt.wantDir)
			}
			if len(p.Unavailable) != len(tt.wantReasons) {
				t.Errorf("Unavailable = %v, want %v", p.Unavailable, tt.wantReasons)
			}
			for f, want := range tt.wantReasons {
				if p.Unavailable[f] != want {
					t.Errorf("Unavailable[%q] = %q, want %q", f, p.Unavailable[f], want)
				}
			}
		})
	}
}

func TestExecRunner_Run(t *testing.T) {
	t.Parallel()

	t.Run("missing command", func(t *testing.T) {
		t.Parallel()
		_, _, _, err := execRunner{}.Run(context.Background(), "portpeek-no-such-command")
		if !errors.Is(err, exec.ErrNotFound) {
			t.Errorf("err = %v, want exec.ErrNotFound", err)
		}
	})

	t.Run("non-zero exit is not an error", func(t *testing.T) {
		t.Parallel()
		stdout, stderr, code, err := execRunner{}.Run(context.Background(), "sh", "-c", "echo out; echo err >&2; exit 3")
		if err != nil || code != 3 || string(stdout) != "out\n" || string(stderr) != "err\n" {
			t.Errorf("Run = %q, %q, %d, %v", stdout, stderr, code, err)
		}
	})

	t.Run("cancelled context", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, _, _, err := execRunner{}.Run(ctx, "sh", "-c", "exit 0")
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	})
}
