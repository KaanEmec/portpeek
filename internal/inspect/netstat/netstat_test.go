package netstat

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/kaanemec/portpeek/internal/inspect"
)

// helperArg makes the test binary act as a child process for execRunner
// tests, which keeps them portable to Windows (no sh).
const helperArg = "portpeek-netstat-helper"

func TestMain(m *testing.M) {
	if len(os.Args) == 3 && os.Args[1] == helperArg {
		_, _ = os.Stdout.WriteString("out")
		_, _ = os.Stderr.WriteString("err")
		code := 0
		if os.Args[2] == "fail" {
			code = 3
		}
		os.Exit(code)
	}
	os.Exit(m.Run())
}

type response struct {
	stdout string
	stderr string
	code   int
	err    error
}

// fakeRunner answers commands from a table keyed by "name arg1 arg2 ..." and
// records every command it is asked to run. Unknown commands fail as if the
// command were not installed.
type fakeRunner struct {
	responses map[string]response

	mu    sync.Mutex
	calls []string
}

func newFakeRunner(responses map[string]response) *fakeRunner {
	return &fakeRunner{responses: responses, calls: []string{}}
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, []byte, int, error) {
	key := strings.Join(append([]string{name}, args...), " ")
	f.mu.Lock()
	f.calls = append(f.calls, key)
	f.mu.Unlock()
	r, ok := f.responses[key]
	if !ok {
		return nil, nil, -1, fmt.Errorf("unexpected command %q: %w", key, exec.ErrNotFound)
	}
	return []byte(r.stdout), []byte(r.stderr), r.code, r.err
}

func (f *fakeRunner) commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// netstatKey is the fakeRunner key of the discovery command for one table.
func netstatKey(table string) string {
	return "netstat -a -n -o -p " + table
}

// psKey is the fakeRunner key of the enrichment command for a PID.
func psKey(pid int) string {
	return "powershell " + strings.Join(processArgs(pid), " ")
}

// netstatResponses answers all four discovery commands, with header-only
// output for every table not in tables.
func netstatResponses(t *testing.T, tables map[string]string) map[string]response {
	t.Helper()
	empty := string(readFixture(t, "empty.txt"))
	responses := map[string]response{}
	for _, table := range []string{"TCP", "TCPv6", "UDP", "UDPv6"} {
		out, ok := tables[table]
		if !ok {
			out = empty
		}
		responses[netstatKey(table)] = response{stdout: out}
	}
	return responses
}

const nodeJSON = `{"Name":"node.exe","CommandLine":"\"C:\\Program Files\\nodejs\\node.exe\" server.js",` +
	`"ExecutablePath":"C:\\Program Files\\nodejs\\node.exe"}`

func TestInspector_Inspect(t *testing.T) {
	t.Parallel()

	responses := netstatResponses(t, map[string]string{
		"TCP":   string(readFixture(t, "dual_stack_tcp.txt")),
		"TCPv6": string(readFixture(t, "dual_stack_tcpv6.txt")),
		"UDP":   string(readFixture(t, "udp_bound.txt")),
	})
	responses[psKey(1234)] = response{stdout: nodeJSON + "\r\n"}
	runner := newFakeRunner(responses)

	got, err := NewWithRunner(runner).Inspect(context.Background(), inspect.Query{Port: 3000})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if got.Query.Port != 3000 {
		t.Errorf("Result.Query = %+v", got.Query)
	}
	if len(got.Owners) != 1 {
		t.Fatalf("Owners = %+v, want 1", got.Owners)
	}
	wantSockets := []inspect.Socket{
		{Protocol: inspect.TCP, Family: inspect.IPv4, Address: "*", Port: 3000, State: inspect.StateListen},
		{Protocol: inspect.TCP, Family: inspect.IPv6, Address: "*", Port: 3000, State: inspect.StateListen},
		{Protocol: inspect.UDP, Family: inspect.IPv4, Address: "*", Port: 3000, State: inspect.StateBound},
	}
	if !reflect.DeepEqual(got.Owners[0].Sockets, wantSockets) {
		t.Errorf("Sockets =\n%+v\nwant\n%+v", got.Owners[0].Sockets, wantSockets)
	}
	wantProcess := inspect.Process{
		PID:     1234,
		Name:    "node.exe",
		Command: `"C:\Program Files\nodejs\node.exe" server.js`,
		Unavailable: map[inspect.Field]string{
			inspect.FieldUser:       reasonNotOnWindows,
			inspect.FieldWorkingDir: reasonNotOnWindows,
		},
	}
	if !reflect.DeepEqual(got.Owners[0].Process, wantProcess) {
		t.Errorf("Process =\n%+v\nwant\n%+v", got.Owners[0].Process, wantProcess)
	}

	wantCalls := []string{netstatKey("TCP"), netstatKey("TCPv6"), netstatKey("UDP"), netstatKey("UDPv6"), psKey(1234)}
	if calls := runner.commands(); !reflect.DeepEqual(calls, wantCalls) {
		t.Errorf("commands =\n%q\nwant\n%q", calls, wantCalls)
	}
}

func TestInspector_Inspect_ProtocolNarrowing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		protocol  inspect.Protocol
		wantCalls []string
		wantProto []inspect.Protocol
	}{
		{
			name:      "both",
			wantCalls: []string{netstatKey("TCP"), netstatKey("TCPv6"), netstatKey("UDP"), netstatKey("UDPv6")},
			wantProto: []inspect.Protocol{inspect.TCP, inspect.UDP, inspect.UDP},
		},
		{
			name:      "tcp",
			protocol:  inspect.TCP,
			wantCalls: []string{netstatKey("TCP"), netstatKey("TCPv6")},
			wantProto: []inspect.Protocol{inspect.TCP},
		},
		{
			name:      "udp",
			protocol:  inspect.UDP,
			wantCalls: []string{netstatKey("UDP"), netstatKey("UDPv6")},
			wantProto: []inspect.Protocol{inspect.UDP, inspect.UDP},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Only the tables the query needs are answered; any other call
			// fails the inspection as a missing tool.
			responses := map[string]response{}
			all := netstatResponses(t, map[string]string{
				"TCP":   string(readFixture(t, "tcp4_listener.txt")),
				"UDP":   string(readFixture(t, "udp_bound.txt")),
				"UDPv6": string(readFixture(t, "udpv6_loopback.txt")),
			})
			for _, key := range tt.wantCalls {
				responses[key] = all[key]
			}
			responses[psKey(1234)] = response{stdout: nodeJSON}
			runner := newFakeRunner(responses)

			q := inspect.Query{Port: 3000, Protocol: tt.protocol}
			got, err := NewWithRunner(runner).Inspect(context.Background(), q)
			if err != nil {
				t.Fatalf("Inspect(%+v): %v", q, err)
			}
			if len(got.Owners) != 1 {
				t.Fatalf("Owners = %+v, want one", got.Owners)
			}
			protos := []inspect.Protocol{}
			for _, s := range got.Owners[0].Sockets {
				protos = append(protos, s.Protocol)
			}
			if !reflect.DeepEqual(protos, tt.wantProto) {
				t.Errorf("socket protocols = %v, want %v", protos, tt.wantProto)
			}
			wantCalls := append(slices.Clone(tt.wantCalls), psKey(1234))
			if calls := runner.commands(); !reflect.DeepEqual(calls, wantCalls) {
				t.Errorf("commands =\n%q\nwant\n%q", calls, wantCalls)
			}
		})
	}
}

func TestInspector_Inspect_TwoOwners(t *testing.T) {
	t.Parallel()

	responses := netstatResponses(t, map[string]string{"TCP": string(readFixture(t, "two_pids.txt"))})
	responses[psKey(1234)] = response{stdout: nodeJSON}
	// PID 5678 exited between netstat and enrichment.
	responses[psKey(5678)] = response{}
	runner := newFakeRunner(responses)

	got, err := NewWithRunner(runner).Inspect(context.Background(), inspect.Query{Port: 3000})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if len(got.Owners) != 2 || got.Owners[0].Process.PID != 1234 || got.Owners[1].Process.PID != 5678 {
		t.Fatalf("Owners = %+v, want 1234 then 5678", got.Owners)
	}
	if got.Owners[0].Process.Name != "node.exe" {
		t.Errorf("first owner Name = %q, want node.exe", got.Owners[0].Process.Name)
	}
	gone := got.Owners[1].Process
	if gone.Unavailable[inspect.FieldName] != reasonExited || gone.Unavailable[inspect.FieldCommand] != reasonExited {
		t.Errorf("exited owner Unavailable = %v", gone.Unavailable)
	}
}

func TestInspector_Inspect_SkipsEnrichment(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		fixture string
		port    int
		pid     int
		want    string // Process.Name
	}{
		{name: "unknown owner", fixture: "pid_zero.txt", port: 3000, pid: unknownPID},
		{name: "System process", fixture: "system_pid.txt", port: 80, pid: systemPID, want: "System"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			responses := netstatResponses(t, map[string]string{"TCP": string(readFixture(t, tt.fixture))})
			responses[psKey(1234)] = response{stdout: nodeJSON}
			runner := newFakeRunner(responses)

			got, err := NewWithRunner(runner).Inspect(context.Background(), inspect.Query{Port: tt.port, Protocol: inspect.TCP})
			if err != nil {
				t.Fatalf("Inspect: %v", err)
			}
			var found *inspect.Process
			for i := range got.Owners {
				if got.Owners[i].Process.PID == tt.pid {
					found = &got.Owners[i].Process
				}
			}
			if found == nil {
				t.Fatalf("pid %d not among owners %+v", tt.pid, got.Owners)
			}
			if found.Name != tt.want {
				t.Errorf("Name = %q, want %q", found.Name, tt.want)
			}
			for _, call := range runner.commands() {
				if call == psKey(tt.pid) {
					t.Errorf("enrichment ran for pid %d", tt.pid)
				}
			}
		})
	}
}

func TestInspector_Inspect_NoMatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		tables map[string]string
	}{
		{name: "header-only output", tables: map[string]string{}},
		{name: "empty output", tables: map[string]string{"TCP": "", "TCPv6": "", "UDP": "", "UDPv6": ""}},
		{
			name: "only connections on the port",
			tables: map[string]string{
				"TCP": "  TCP    127.0.0.1:3000   127.0.0.1:52000   ESTABLISHED   1234\r\n" +
					"  TCP    127.0.0.1:3000   127.0.0.1:51999   TIME_WAIT     0\r\n",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runner := newFakeRunner(netstatResponses(t, tt.tables))
			got, err := NewWithRunner(runner).Inspect(context.Background(), inspect.Query{Port: 3000})
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
		table    string // the table whose command misbehaves
		resp     response
		wantKind inspect.Kind
		wantText string
	}{
		{
			name:     "netstat not installed",
			table:    "TCP",
			resp:     response{code: -1, err: &exec.Error{Name: "netstat", Err: exec.ErrNotFound}},
			wantKind: inspect.KindToolMissing,
		},
		{
			name:     "exit 1 with stderr",
			table:    "TCP",
			resp:     response{code: 1, stderr: "The requested protocol is not supported.\r\n"},
			wantKind: inspect.KindCommandFailed,
			wantText: "exit status 1: The requested protocol is not supported.",
		},
		{
			name:     "exit code without stderr",
			table:    "UDP",
			resp:     response{code: 2},
			wantKind: inspect.KindCommandFailed,
			wantText: "exit status 2",
		},
		{
			name:     "later table fails",
			table:    "UDPv6",
			resp:     response{code: 1, stderr: "boom"},
			wantKind: inspect.KindCommandFailed,
			wantText: "boom",
		},
		{
			name:     "unparseable output",
			table:    "TCPv6",
			resp:     response{stdout: "  TCP    [::]:3000   [::]:0   LISTENING   x\r\n"},
			wantKind: inspect.KindCommandFailed,
			wantText: "parsing TCPv6 output",
		},
		{
			name:     "command did not run",
			table:    "TCP",
			resp:     response{code: -1, err: errors.New("signal: killed")},
			wantKind: inspect.KindCommandFailed,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			responses := netstatResponses(t, map[string]string{})
			responses[netstatKey(tt.table)] = tt.resp
			_, err := NewWithRunner(newFakeRunner(responses)).Inspect(context.Background(), inspect.Query{Port: 3000})
			if err == nil {
				t.Fatal("Inspect: expected error")
			}
			ie, ok := errors.AsType[*inspect.Error](err)
			if !ok || ie.Kind != tt.wantKind || ie.Op != "netstat" {
				t.Errorf("Inspect error = %#v, want *inspect.Error of kind %q for netstat", err, tt.wantKind)
			}
			if !strings.Contains(err.Error(), tt.wantText) {
				t.Errorf("error %q does not contain %q", err, tt.wantText)
			}
		})
	}
}

func TestInspector_Inspect_InvalidQuery(t *testing.T) {
	t.Parallel()
	runner := newFakeRunner(map[string]response{})
	if _, err := NewWithRunner(runner).Inspect(context.Background(), inspect.Query{Port: 0}); err == nil {
		t.Fatal("Inspect: expected error for port 0")
	}
	if calls := runner.commands(); len(calls) != 0 {
		t.Errorf("commands = %q, want none for an invalid query", calls)
	}
}

// cancellingRunner answers from a fakeRunner and cancels the context after
// running the command named by cancelAfter, so that command succeeds but
// the inspection was interrupted.
type cancellingRunner struct {
	*fakeRunner
	cancelAfter string
	cancel      context.CancelFunc
}

func (c cancellingRunner) Run(ctx context.Context, name string, args ...string) ([]byte, []byte, int, error) {
	if name == c.cancelAfter {
		defer c.cancel()
	}
	return c.fakeRunner.Run(ctx, name, args...)
}

func TestInspector_Inspect_Cancelled(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		cancelAfter string
	}{
		{name: "during discovery", cancelAfter: "netstat"},
		{name: "during enrichment", cancelAfter: "powershell"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)
			responses := netstatResponses(t, map[string]string{"TCP": string(readFixture(t, "two_pids.txt"))})
			responses[psKey(1234)] = response{stdout: nodeJSON}
			responses[psKey(5678)] = response{stdout: nodeJSON}
			runner := cancellingRunner{fakeRunner: newFakeRunner(responses), cancelAfter: tt.cancelAfter, cancel: cancel}

			got, err := NewWithRunner(runner).Inspect(ctx, inspect.Query{Port: 3000, Protocol: inspect.TCP})

			if _, isInspect := errors.AsType[*inspect.Error](err); !errors.Is(err, context.Canceled) || isInspect {
				t.Errorf("Inspect error = %#v, want plain context.Canceled", err)
			}
			if len(got.Owners) != 0 {
				t.Errorf("Owners = %+v, want none on cancellation", got.Owners)
			}
			// Nothing runs after the command that saw the cancellation.
			calls := runner.commands()
			matching := 0
			for _, call := range calls {
				if strings.HasPrefix(call, tt.cancelAfter+" ") {
					matching++
				}
			}
			if matching != 1 || !strings.HasPrefix(calls[len(calls)-1], tt.cancelAfter+" ") {
				t.Errorf("commands = %q, want exactly one %s call, last", calls, tt.cancelAfter)
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

	t.Run("output and zero exit", func(t *testing.T) {
		t.Parallel()
		stdout, stderr, code, err := execRunner{}.Run(context.Background(), os.Args[0], helperArg, "ok")
		if err != nil || code != 0 || string(stdout) != "out" || string(stderr) != "err" {
			t.Errorf("Run = %q, %q, %d, %v", stdout, stderr, code, err)
		}
	})

	t.Run("non-zero exit is not an error", func(t *testing.T) {
		t.Parallel()
		stdout, stderr, code, err := execRunner{}.Run(context.Background(), os.Args[0], helperArg, "fail")
		if err != nil || code != 3 || string(stdout) != "out" || string(stderr) != "err" {
			t.Errorf("Run = %q, %q, %d, %v", stdout, stderr, code, err)
		}
	})

	t.Run("cancelled context", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, _, _, err := execRunner{}.Run(ctx, os.Args[0], helperArg, "ok")
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	})
}
