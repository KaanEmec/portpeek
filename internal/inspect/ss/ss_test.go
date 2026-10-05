package ss

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

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

// findKey is the fakeRunner key of the discovery command for a port queried
// for both protocols.
func findKey(port int) string {
	return fmt.Sprintf("ss -H -a -n -p -t -u sport = :%d", port)
}

// listKey is the fakeRunner key of the List discovery command.
const listKey = "ss -H -a -n -p -t -u"

// newTestInspector returns an Inspector running r and reading procRoot.
func newTestInspector(r Runner, procRoot string) *Inspector {
	i := NewWithRunner(r)
	i.procRoot = procRoot
	return i
}

func TestInspector_Inspect(t *testing.T) {
	t.Parallel()

	runner := fakeRunner{findKey(48123): {stdout: string(readFixture(t, "established_mixed.txt"))}}
	got, err := newTestInspector(runner, procTree(t)).Inspect(context.Background(), inspect.Query{Port: 48123})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if len(got.Owners) != 1 {
		t.Fatalf("Inspect owners = %+v, want 1", got.Owners)
	}
	p := got.Owners[0].Process
	if p.PID != 1005 || p.Name != "sockets" || p.User != "root" {
		t.Errorf("process identity = %+v", p)
	}
	if p.Command != "/tmp/sockets eth0" {
		t.Errorf("Command = %q", p.Command)
	}
	if p.WorkingDir != fixtureCwd {
		t.Errorf("WorkingDir = %q, want %q", p.WorkingDir, fixtureCwd)
	}
	if len(p.Unavailable) != 0 {
		t.Errorf("Unavailable = %v, want empty", p.Unavailable)
	}
	if got.Query.Port != 48123 {
		t.Errorf("Result.Query = %+v", got.Query)
	}
}

func TestInspector_Inspect_ExitedOwner(t *testing.T) {
	t.Parallel()

	// PID 1013 has no directory in the fixture tree, as if it exited
	// between ss and enrichment.
	runner := fakeRunner{findKey(48126): {stdout: string(readFixture(t, "two_pids.txt"))}}
	got, err := newTestInspector(runner, procTree(t)).Inspect(context.Background(), inspect.Query{Port: 48126})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if len(got.Owners) != 2 || got.Owners[1].Process.PID != 1013 {
		t.Fatalf("Owners = %+v, want 1005 and 1013", got.Owners)
	}
	p := got.Owners[1].Process
	if p.Name != "exe" {
		t.Errorf("Name = %q, want the name ss reported", p.Name)
	}
	want := map[inspect.Field]string{
		inspect.FieldUser:       reasonExited,
		inspect.FieldCommand:    reasonExited,
		inspect.FieldWorkingDir: reasonExited,
	}
	if fmt.Sprint(p.Unavailable) != fmt.Sprint(want) {
		t.Errorf("Unavailable = %v, want %v", p.Unavailable, want)
	}
}

func TestInspector_Inspect_UnknownOwner(t *testing.T) {
	t.Parallel()

	runner := fakeRunner{findKey(48123): {stdout: string(readFixture(t, "no_users.txt"))}}
	// procfs is never read for the unknown owner.
	missing := filepath.Join(t.TempDir(), "no-proc")
	got, err := newTestInspector(runner, missing).Inspect(context.Background(), inspect.Query{Port: 48123})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if len(got.Owners) != 1 {
		t.Fatalf("Owners = %+v, want one unknown owner", got.Owners)
	}
	p := got.Owners[0].Process
	if p.PID != 0 || p.Name != "" || p.Command != "" {
		t.Errorf("Process = %+v, want PID 0 with no details", p)
	}
	for _, f := range []inspect.Field{
		inspect.FieldName,
		inspect.FieldUser,
		inspect.FieldCommand,
		inspect.FieldWorkingDir,
	} {
		if p.Unavailable[f] != reasonHidden {
			t.Errorf("Unavailable[%q] = %q, want %q", f, p.Unavailable[f], reasonHidden)
		}
	}
}

func TestInspector_Inspect_ProtocolNarrowing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		protocol inspect.Protocol
		key      string
		stdout   string
		want     int
	}{
		{name: "both", key: findKey(48128), stdout: string(readFixture(t, "dual_stack_wildcard.txt")), want: 2},
		{
			name:     "tcp",
			protocol: inspect.TCP,
			key:      "ss -H -a -n -p -t sport = :48128",
			stdout:   string(readFixture(t, "tcp_only_no_netid.txt")),
			want:     1,
		},
		{
			name:     "udp",
			protocol: inspect.UDP,
			key:      "ss -H -a -n -p -u sport = :48128",
			stdout:   `UNCONN 0      0      *:48128 *:* users:(("sockets",pid=945,fd=5))` + "\n",
			want:     1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runner := fakeRunner{tt.key: {stdout: tt.stdout}}
			q := inspect.Query{Port: 48128, Protocol: tt.protocol}
			got, err := newTestInspector(runner, t.TempDir()).Inspect(context.Background(), q)
			if err != nil {
				t.Fatalf("Inspect(%+v): %v", q, err)
			}
			if len(got.Owners) != 1 || len(got.Owners[0].Sockets) != tt.want {
				t.Fatalf("Owners = %+v, want one owner with %d sockets", got.Owners, tt.want)
			}
			for _, s := range got.Owners[0].Sockets {
				if tt.protocol != "" && s.Protocol != tt.protocol {
					t.Errorf("socket %+v does not match protocol %q", s, tt.protocol)
				}
			}
		})
	}
}

func TestInspector_Inspect_NoMatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		stdout string
	}{
		{name: "empty output", stdout: ""},
		{
			name:   "only an established connection",
			stdout: "tcp ESTAB 0 0 127.0.0.1:48123 127.0.0.1:60166 users:((\"sockets\",pid=1005,fd=8))\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runner := fakeRunner{findKey(48123): {stdout: tt.stdout}}
			got, err := newTestInspector(runner, t.TempDir()).Inspect(context.Background(), inspect.Query{Port: 48123})
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
			name:     "ss not installed",
			resp:     response{code: -1, err: &exec.Error{Name: "ss", Err: exec.ErrNotFound}},
			wantKind: inspect.KindToolMissing,
		},
		{
			name:     "permission denied on stderr",
			resp:     response{code: 1, stderr: "Cannot open netlink socket: Permission denied\n"},
			wantKind: inspect.KindPermissionDenied,
		},
		{
			name:     "operation not permitted on stderr",
			resp:     response{code: 1, stderr: "Cannot send dump request: Operation not permitted\n"},
			wantKind: inspect.KindPermissionDenied,
		},
		{
			// Observed with iproute2 6.15.0 for a malformed filter.
			name: "exit 1 with stderr",
			resp: response{
				code:   1,
				stdout: "Cannot parse dst/src address.\n",
				stderr: "Error: \"abc\" does not look like a port.\n",
			},
			wantKind: inspect.KindCommandFailed,
			wantText: "does not look like a port",
		},
		{
			name:     "unsupported option on an old ss",
			resp:     response{code: 255, stderr: "ss: invalid option -- 'H'\n"},
			wantKind: inspect.KindCommandFailed,
			wantText: "invalid option",
		},
		{
			name:     "exit code without stderr",
			resp:     response{code: 2},
			wantKind: inspect.KindCommandFailed,
			wantText: "exit status 2",
		},
		{
			name:     "unparseable output",
			resp:     response{stdout: "tcp LISTEN 0 128 127.0.0.1:3000 0.0.0.0:* users:((\"a\",pid=x,fd=3))\n"},
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
			runner := fakeRunner{findKey(3000): tt.resp}
			_, err := newTestInspector(runner, t.TempDir()).Inspect(context.Background(), inspect.Query{Port: 3000})
			if err == nil {
				t.Fatal("Inspect: expected error")
			}
			var ie *inspect.Error
			if !errors.As(err, &ie) || ie.Kind != tt.wantKind || ie.Op != "ss" {
				t.Errorf("Inspect error = %#v, want *inspect.Error of kind %q for ss", err, tt.wantKind)
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

// cancellingRunner answers from a fakeRunner and cancels the context after
// running a command, so the command itself succeeds but the inspection was
// interrupted.
type cancellingRunner struct {
	fakeRunner
	cancel context.CancelFunc
}

func (c cancellingRunner) Run(ctx context.Context, name string, args ...string) ([]byte, []byte, int, error) {
	defer c.cancel()
	return c.fakeRunner.Run(ctx, name, args...)
}

func TestInspector_Inspect_Cancelled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	runner := cancellingRunner{
		fakeRunner: fakeRunner{findKey(48123): {stdout: string(readFixture(t, "tcp4_listener.txt"))}},
		cancel:     cancel,
	}

	got, err := newTestInspector(runner, procTree(t)).Inspect(ctx, inspect.Query{Port: 48123})

	var ie *inspect.Error
	if !errors.Is(err, context.Canceled) || errors.As(err, &ie) {
		t.Errorf("Inspect error = %#v, want plain context.Canceled", err)
	}
	if len(got.Owners) != 0 {
		t.Errorf("Owners = %+v, want none on cancellation", got.Owners)
	}
}

// all_sockets.txt is not a recording: ss cannot run on the macOS machine
// where List was written. It is assembled from rows of the recorded
// fixtures (tcp4_listener, established_mixed, dual_stack_same_pid,
// udp_unconn, two_pids, link_local, dual_stack_wildcard,
// tcp_bound_not_listening, no_users with its port moved to 48133) plus two
// constructed rows: the client end of the established connection (PID 1020)
// and a connected UDP socket (PID 1030).
func TestInspector_List(t *testing.T) {
	t.Parallel()

	runner := fakeRunner{listKey: {stdout: string(readFixture(t, "all_sockets.txt"))}}
	// procfs is never read by List.
	missing := filepath.Join(t.TempDir(), "no-proc")
	before := time.Now()
	got, err := newTestInspector(runner, missing).List(context.Background())
	after := time.Now()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got.Taken.Before(before) || got.Taken.After(after) {
		t.Errorf("Taken = %v, want between %v and %v", got.Taken, before, after)
	}

	// PIDs 1020 and 1030 hold only connections. User, Command and
	// WorkingDir stay unset; only the unknown owner has fields marked
	// unavailable, and only Name and User.
	unknown := inspect.Process{
		PID: unknownPID,
		Unavailable: map[inspect.Field]string{
			inspect.FieldName: reasonHidden,
			inspect.FieldUser: reasonHidden,
		},
	}
	want := []inspect.Owner{
		{
			Process: proc(945, "sockets"),
			Sockets: []inspect.Socket{
				udpBound(inspect.IPv6, "fe80::1234%eth0", 48127),
				tcpListen(inspect.IPv6, "*", 48128),
				udpBound(inspect.IPv6, "*", 48128),
				tcpBound(inspect.IPv4, "127.0.0.1", 48132),
			},
		},
		{
			Process: proc(1005, "sockets"),
			Sockets: []inspect.Socket{
				tcpListen(inspect.IPv4, "127.0.0.1", 48123),
				tcpListen(inspect.IPv4, "*", 48124),
				tcpListen(inspect.IPv6, "*", 48124),
				udpBound(inspect.IPv4, "*", 48125),
				udpBound(inspect.IPv6, "::1", 48125),
				tcpListen(inspect.IPv4, "127.0.0.1", 48126),
			},
		},
		{
			Process: proc(1013, "exe"),
			Sockets: []inspect.Socket{tcpListen(inspect.IPv4, "127.0.0.1", 48126)},
		},
		{
			Process: unknown,
			Sockets: []inspect.Socket{tcpListen(inspect.IPv4, "127.0.0.1", 48133)},
		},
	}
	if !reflect.DeepEqual(got.Owners, want) {
		t.Errorf("Owners =\n%+v\nwant\n%+v", got.Owners, want)
	}
}

func TestInspector_List_NoSockets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		stdout string
	}{
		{name: "empty output", stdout: ""},
		{
			name:   "only an established connection",
			stdout: "tcp ESTAB 0 0 127.0.0.1:48123 127.0.0.1:60166 users:((\"sockets\",pid=1005,fd=8))\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runner := fakeRunner{listKey: {stdout: tt.stdout}}
			got, err := newTestInspector(runner, t.TempDir()).List(context.Background())
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if got.Owners == nil || len(got.Owners) != 0 {
				t.Errorf("Owners = %#v, want empty non-nil slice", got.Owners)
			}
			if got.Taken.IsZero() {
				t.Error("Taken is zero")
			}
		})
	}
}

func TestInspector_List_Errors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		resp     response
		wantKind inspect.Kind
		wantText string
	}{
		{
			name:     "ss not installed",
			resp:     response{code: -1, err: &exec.Error{Name: "ss", Err: exec.ErrNotFound}},
			wantKind: inspect.KindToolMissing,
		},
		{
			name:     "permission denied on stderr",
			resp:     response{code: 1, stderr: "Cannot open netlink socket: Permission denied\n"},
			wantKind: inspect.KindPermissionDenied,
		},
		{
			name:     "unsupported option on an old ss",
			resp:     response{code: 255, stderr: "ss: invalid option -- 'H'\n"},
			wantKind: inspect.KindCommandFailed,
			wantText: "invalid option",
		},
		{
			name:     "row without a Netid column",
			resp:     response{stdout: "LISTEN 0 4096 *:48128 *:* users:((\"sockets\",pid=945,fd=4))\n"},
			wantKind: inspect.KindCommandFailed,
			wantText: "missing Netid column",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runner := fakeRunner{listKey: tt.resp}
			_, err := newTestInspector(runner, t.TempDir()).List(context.Background())
			ie, ok := errors.AsType[*inspect.Error](err)
			if !ok || ie.Kind != tt.wantKind || ie.Op != "ss" {
				t.Fatalf("List error = %#v, want *inspect.Error of kind %q for ss", err, tt.wantKind)
			}
			if !strings.Contains(err.Error(), tt.wantText) {
				t.Errorf("error %q does not contain %q", err, tt.wantText)
			}
		})
	}
}

func TestInspector_List_Cancelled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	runner := cancellingRunner{
		fakeRunner: fakeRunner{listKey: {stdout: string(readFixture(t, "all_sockets.txt"))}},
		cancel:     cancel,
	}

	got, err := newTestInspector(runner, t.TempDir()).List(ctx)

	if _, isInspect := errors.AsType[*inspect.Error](err); !errors.Is(err, context.Canceled) || isInspect {
		t.Errorf("List error = %#v, want plain context.Canceled", err)
	}
	if len(got.Owners) != 0 {
		t.Errorf("Owners = %+v, want none on cancellation", got.Owners)
	}
}

func tcpListen(family inspect.Family, addr string, port int) inspect.Socket {
	return inspect.Socket{Protocol: inspect.TCP, Family: family, Address: addr, Port: port, State: inspect.StateListen}
}

func tcpBound(family inspect.Family, addr string, port int) inspect.Socket {
	return inspect.Socket{Protocol: inspect.TCP, Family: family, Address: addr, Port: port, State: inspect.StateBound}
}

func udpBound(family inspect.Family, addr string, port int) inspect.Socket {
	return inspect.Socket{Protocol: inspect.UDP, Family: family, Address: addr, Port: port, State: inspect.StateBound}
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
