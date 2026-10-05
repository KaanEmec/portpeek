package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/kaanemec/portpeek/internal/inspect"
)

// fakeInspector returns a fixed result and error and records the query.
// When results is set, call n returns results[n], repeating the last one, so
// the --stop recheck can see a different answer from the first inspection.
type fakeInspector struct {
	result  inspect.Result
	results []inspect.Result
	err     error
	called  bool
	calls   int
	got     inspect.Query
}

func (f *fakeInspector) Inspect(_ context.Context, q inspect.Query) (inspect.Result, error) {
	f.called = true
	f.got = q
	res := f.result
	if len(f.results) > 0 {
		res = f.results[min(f.calls, len(f.results)-1)]
	}
	f.calls++
	return res, f.err
}

// setPrivileged overrides privileged for the rest of the test. Callers must
// not run in parallel with other tests.
func setPrivileged(t *testing.T, v bool) {
	t.Helper()
	old := privileged
	privileged = v
	t.Cleanup(func() { privileged = old })
}

func nodeOwner() inspect.Owner {
	return inspect.Owner{
		Process: inspect.Process{
			PID:         48213,
			Name:        "node",
			User:        "kaanemec",
			Command:     "node server.js",
			WorkingDir:  "/Users/kaanemec/app",
			Unavailable: map[inspect.Field]string{},
		},
		Sockets: []inspect.Socket{
			{Protocol: inspect.TCP, Family: inspect.IPv4, Address: "127.0.0.1", Port: 3000, State: inspect.StateListen},
		},
	}
}

// unknownOwner is a socket that an adapter listed without being able to
// attribute it to a process, as the Linux adapter reports for other users'
// sockets without root.
func unknownOwner() inspect.Owner {
	p := inspect.Process{Unavailable: map[inspect.Field]string{}}
	for _, f := range []inspect.Field{
		inspect.FieldName,
		inspect.FieldUser,
		inspect.FieldCommand,
		inspect.FieldWorkingDir,
	} {
		p.MarkUnavailable(f, "not readable without elevated privileges")
	}
	return inspect.Owner{
		Process: p,
		Sockets: []inspect.Socket{
			{Protocol: inspect.TCP, Family: inspect.IPv4, Address: "127.0.0.1", Port: 3000, State: inspect.StateListen},
		},
	}
}

// unknownText is the default view of unknownOwner, before its hint.
const unknownText = `3000/tcp  unknown process
  127.0.0.1:3000   listening   loopback only
`

var nodeText = "3000/tcp  node  (PID 48213)\n" +
	"  127.0.0.1:3000   listening   loopback only\n" +
	"  node server.js\n" +
	stop(48213)

const nodeJSON = `{
  "schema": 1,
  "query": {
    "port": 3000,
    "protocol": ""
  },
  "complete": true,
  "owners": [
    {
      "process": {
        "pid": 48213,
        "name": "node",
        "user": "kaanemec",
        "command": "node server.js",
        "working_dir": "/Users/kaanemec/app",
        "unavailable": {}
      },
      "sockets": [
        {
          "protocol": "tcp",
          "family": "ipv4",
          "address": "127.0.0.1",
          "port": 3000,
          "state": "LISTEN",
          "exposure": "loopback"
        }
      ]
    }
  ]
}
`

func TestRun(t *testing.T) {
	setPrivileged(t, true)

	udpOnly := nodeOwner()
	udpOnly.Sockets = []inspect.Socket{udpSocket(inspect.IPv4, "*", 5353)}

	unavailableJSON := nodeOwner()
	unavailableJSON.Process.Command = ""
	unavailableJSON.Process.MarkUnavailable(inspect.FieldCommand, "permission denied")

	nodeDetail := newTextView(defaultWidth, false).detail(inspect.Query{Port: 3000}, []inspect.Owner{nodeOwner()})

	tests := []struct {
		name       string
		args       []string
		owners     []inspect.Owner
		inspectErr error
		wantCode   int
		wantOut    string
		wantErr    string
		wantQuery  *inspect.Query
	}{
		{
			name:      "single owner text",
			args:      []string{"3000"},
			owners:    []inspect.Owner{nodeOwner()},
			wantCode:  0,
			wantOut:   nodeText,
			wantQuery: &inspect.Query{Port: 3000},
		},
		{
			name:      "flags after port",
			args:      []string{"3000", "--tcp"},
			owners:    []inspect.Owner{nodeOwner()},
			wantCode:  0,
			wantOut:   nodeText,
			wantQuery: &inspect.Query{Port: 3000, Protocol: inspect.TCP},
		},
		{
			name:      "flags before port",
			args:      []string{"--udp", "5353"},
			owners:    []inspect.Owner{udpOnly},
			wantCode:  0,
			wantQuery: &inspect.Query{Port: 5353, Protocol: inspect.UDP},
			wantOut: "5353/udp  node  (PID 48213)\n" +
				"  *:5353   bound   all interfaces\n" +
				"  node server.js\n" +
				stop(48213),
		},
		{
			name:     "several owners",
			args:     []string{"3000"},
			owners:   []inspect.Owner{nodeOwner(), pythonOwner()},
			wantCode: 0,
			wantOut: `3000/tcp  2 processes
  node      PID 48213  127.0.0.1:3000   loopback only
  python3   PID 500    *:3000           all interfaces
`,
		},
		{
			name:     "detail",
			args:     []string{"3000", "--detail"},
			owners:   []inspect.Owner{nodeOwner()},
			wantCode: 0,
			wantOut:  nodeDetail,
		},
		{
			name:     "detail before port",
			args:     []string{"--detail", "3000"},
			owners:   []inspect.Owner{nodeOwner()},
			wantCode: 0,
			wantOut:  nodeDetail,
		},
		{
			name:     "detail is ignored with json",
			args:     []string{"3000", "--detail", "--json"},
			owners:   []inspect.Owner{nodeOwner()},
			wantCode: 0,
			wantOut:  nodeJSON,
		},
		{
			name:     "detail no match",
			args:     []string{"3000", "--detail"},
			wantCode: 1,
			wantOut:  "no listening or bound socket on 3000 (tcp or udp)\n",
		},
		{
			name:     "unknown owner text",
			args:     []string{"3000"},
			owners:   []inspect.Owner{unknownOwner()},
			wantCode: 0,
			wantOut:  unknownText + unknownOwnerHint + "\n",
		},
		{
			name:     "unknown owner json is incomplete",
			args:     []string{"3000", "--json"},
			owners:   []inspect.Owner{unknownOwner()},
			wantCode: 0,
			wantOut: `{
  "schema": 1,
  "query": {
    "port": 3000,
    "protocol": ""
  },
  "complete": false,
  "owners": [
    {
      "process": {
        "pid": 0,
        "name": "",
        "user": "",
        "command": "",
        "working_dir": "",
        "unavailable": {
          "command": "not readable without elevated privileges",
          "name": "not readable without elevated privileges",
          "user": "not readable without elevated privileges",
          "working_dir": "not readable without elevated privileges"
        }
      },
      "sockets": [
        {
          "protocol": "tcp",
          "family": "ipv4",
          "address": "127.0.0.1",
          "port": 3000,
          "state": "LISTEN",
          "exposure": "loopback"
        }
      ]
    }
  ]
}
`,
		},
		{
			name:     "no match",
			args:     []string{"3000"},
			wantCode: 1,
			wantOut:  "no listening or bound socket on 3000 (tcp or udp)\n",
		},
		{
			name:     "no match with protocol",
			args:     []string{"--udp", "53"},
			wantCode: 1,
			wantOut:  "no listening or bound socket on 53 (udp)\n",
		},
		{
			name:     "json match",
			args:     []string{"--json", "3000"},
			owners:   []inspect.Owner{nodeOwner()},
			wantCode: 0,
			wantOut:  nodeJSON,
		},
		{
			name:     "json unavailable fields",
			args:     []string{"3000", "--json", "--tcp"},
			owners:   []inspect.Owner{unavailableJSON},
			wantCode: 0,
			wantOut: strings.Replace(
				strings.Replace(nodeJSON, `"protocol": ""`, `"protocol": "tcp"`, 1),
				`"command": "node server.js",
        "working_dir": "/Users/kaanemec/app",
        "unavailable": {}`,
				`"command": "",
        "working_dir": "/Users/kaanemec/app",
        "unavailable": {
          "command": "permission denied"
        }`,
				1,
			),
		},
		{
			name:     "json no match",
			args:     []string{"3000", "--json"},
			wantCode: 1,
			wantOut: `{
  "schema": 1,
  "query": {
    "port": 3000,
    "protocol": ""
  },
  "complete": true,
  "owners": []
}
`,
		},
		{
			name:       "json error",
			args:       []string{"3000", "--json"},
			inspectErr: &inspect.Error{Kind: inspect.KindPermissionDenied, Op: "lsof"},
			wantCode:   3,
			wantOut: `{
  "schema": 1,
  "error": {
    "kind": "permission-denied",
    "message": "permission denied while inspecting port 3000."
  }
}
`,
		},
		{
			name:       "interrupted",
			args:       []string{"3000"},
			inspectErr: context.Canceled,
			wantCode:   130,
			wantErr:    "portpeek: interrupted\n",
		},
		{
			name:       "interrupted with json",
			args:       []string{"3000", "--json"},
			inspectErr: fmt.Errorf("running lsof: %w", context.Canceled),
			wantCode:   130,
			wantErr:    "portpeek: interrupted\n",
		},
		{
			name:     "help flag",
			args:     []string{"--help"},
			wantCode: 0,
			wantOut:  helpText,
		},
		{
			name:     "short help flag",
			args:     []string{"-h"},
			wantCode: 0,
			wantOut:  helpText,
		},
		{
			name:     "help after invalid port",
			args:     []string{"abc", "--help"},
			wantCode: 0,
			wantOut:  helpText,
		},
		{
			name:     "version",
			args:     []string{"--version"},
			wantCode: 0,
			wantOut:  "portpeek dev\n",
		},
		{
			name:     "version with a one-port flag",
			args:     []string{"--json", "--version"},
			wantCode: 0,
			wantOut:  "portpeek dev\n",
		},
		{
			name:     "help with a one-port flag",
			args:     []string{"--json", "--help"},
			wantCode: 0,
			wantOut:  helpText,
		},
		{
			name:     "invalid pid without a port",
			args:     []string{"--pid", "abc"},
			wantCode: 2,
			wantErr: "portpeek: invalid value \"abc\" for flag -pid: must be a positive process ID\n" +
				"Try 'portpeek --help' for usage.\n",
		},
		{
			name:     "non numeric port",
			args:     []string{"http"},
			wantCode: 2,
			wantErr:  "portpeek: invalid port \"http\": must be a number between 1 and 65535\nTry 'portpeek --help' for usage.\n",
		},
		{
			name:     "port zero",
			args:     []string{"0"},
			wantCode: 2,
			wantErr:  "portpeek: invalid port 0: must be between 1 and 65535\nTry 'portpeek --help' for usage.\n",
		},
		{
			name:     "port too large",
			args:     []string{"65536"},
			wantCode: 2,
			wantErr:  "portpeek: invalid port 65536: must be between 1 and 65535\nTry 'portpeek --help' for usage.\n",
		},
		{
			name:     "negative port",
			args:     []string{"-1"},
			wantCode: 2,
			wantErr:  "portpeek: flag provided but not defined: -1\nTry 'portpeek --help' for usage.\n",
		},
		{
			name:     "negative port after separator",
			args:     []string{"--", "-1"},
			wantCode: 2,
			wantErr:  "portpeek: invalid port -1: must be between 1 and 65535\nTry 'portpeek --help' for usage.\n",
		},
		{
			name:     "both protocols",
			args:     []string{"3000", "--tcp", "--udp"},
			wantCode: 2,
			wantErr:  "portpeek: --tcp and --udp cannot be used together\nTry 'portpeek --help' for usage.\n",
		},
		{
			name:     "extra argument",
			args:     []string{"3000", "4000"},
			wantCode: 2,
			wantErr:  "portpeek: expected one port, got 2 arguments\nTry 'portpeek --help' for usage.\n",
		},
		{
			name:     "unknown flag",
			args:     []string{"3000", "--bogus"},
			wantCode: 2,
			wantErr:  "portpeek: flag provided but not defined: -bogus\nTry 'portpeek --help' for usage.\n",
		},
		{
			name:       "tool missing",
			args:       []string{"3000"},
			inspectErr: &inspect.Error{Kind: inspect.KindToolMissing, Op: "lsof", Err: errors.New("not found")},
			wantCode:   3,
			wantErr: "portpeek: required tool \"lsof\" is not installed, so port 3000 cannot be inspected.\n" +
				"Install it and make sure it is on your PATH.\n",
		},
		{
			name:       "ss missing names its package",
			args:       []string{"3000"},
			inspectErr: &inspect.Error{Kind: inspect.KindToolMissing, Op: "ss", Err: errors.New("not found")},
			wantCode:   3,
			wantErr: "portpeek: required tool \"ss\" is not installed, so port 3000 cannot be inspected.\n" +
				"Install it (part of the iproute2 package) and make sure it is on your PATH.\n",
		},
		{
			name:       "permission denied",
			args:       []string{"3000"},
			inspectErr: &inspect.Error{Kind: inspect.KindPermissionDenied, Op: "lsof"},
			wantCode:   3,
			wantErr: "portpeek: permission denied while inspecting port 3000.\n" +
				"Re-run with sudo for full details: sudo portpeek 3000\n",
		},
		{
			name:       "command failed",
			args:       []string{"3000"},
			inspectErr: &inspect.Error{Kind: inspect.KindCommandFailed, Op: "lsof", Err: errors.New("exit status 2")},
			wantCode:   3,
			wantErr:    "portpeek: lsof: command-failed: exit status 2\n",
		},
		{
			name:       "unsupported platform",
			args:       []string{"3000"},
			inspectErr: &inspect.Error{Kind: inspect.KindUnsupported, Op: "inspect", Err: errors.New("plan9")},
			wantCode:   3,
			wantErr:    "portpeek: inspect: unsupported-platform: plan9\n",
		},
		{
			name:       "plain error",
			args:       []string{"3000"},
			inspectErr: errors.New("boom"),
			wantCode:   3,
			wantErr:    "portpeek: boom\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fake := &fakeInspector{
				result: inspect.Result{Owners: tt.owners},
				err:    tt.inspectErr,
			}
			var stdout, stderr bytes.Buffer

			code := Run(t.Context(), tt.args, &stdout, &stderr, Deps{Inspector: fake})

			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d", code, tt.wantCode)
			}
			if got := stdout.String(); got != tt.wantOut {
				t.Errorf("stdout mismatch\n got:\n%s\nwant:\n%s", got, tt.wantOut)
			}
			if got := stderr.String(); got != tt.wantErr {
				t.Errorf("stderr mismatch\n got:\n%s\nwant:\n%s", got, tt.wantErr)
			}
			if tt.wantQuery != nil && fake.got != *tt.wantQuery {
				t.Errorf("query = %+v, want %+v", fake.got, *tt.wantQuery)
			}
			if tt.wantCode == 2 && fake.called {
				t.Error("inspector was called for invalid input")
			}
		})
	}
}

func TestRun_JSONNeverNull(t *testing.T) {
	t.Parallel()

	owner := nodeOwner()
	owner.Sockets = nil
	owner.Process.Unavailable = nil
	fake := &fakeInspector{result: inspect.Result{Owners: []inspect.Owner{owner}}}
	var stdout, stderr bytes.Buffer

	code := Run(t.Context(), []string{"3000", "--json"}, &stdout, &stderr, Deps{Inspector: fake})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if strings.Contains(stdout.String(), "null") {
		t.Errorf("JSON contains null:\n%s", stdout.String())
	}
}

func TestRun_Version(t *testing.T) {
	old := Version
	Version = "1.2.3"
	t.Cleanup(func() { Version = old })

	var stdout, stderr bytes.Buffer
	code := Run(t.Context(), []string{"--version"}, &stdout, &stderr, Deps{Inspector: &fakeInspector{}})

	if code != 0 || stdout.String() != "portpeek 1.2.3\n" {
		t.Errorf("got code %d, stdout %q", code, stdout.String())
	}
}

func TestRun_Unprivileged(t *testing.T) {
	setPrivileged(t, false)

	tests := []struct {
		name     string
		args     []string
		owners   []inspect.Owner
		wantCode int
		wantOut  string
	}{
		{
			name:     "no match text",
			args:     []string{"3000"},
			wantCode: exitNoMatch,
			wantOut:  "no listening or bound socket on 3000 (tcp or udp)\n" + hiddenSocketsHint + "\n",
		},
		{
			name:     "match text",
			args:     []string{"3000"},
			owners:   []inspect.Owner{nodeOwner()},
			wantCode: exitOK,
			wantOut:  nodeText + hiddenSocketsHint + "\n",
		},
		{
			name:     "unknown owner hint replaces hidden sockets hint",
			args:     []string{"3000"},
			owners:   []inspect.Owner{unknownOwner()},
			wantCode: exitOK,
			wantOut:  unknownText + unknownOwnerHint + "\n",
		},
		{
			name:     "match json",
			args:     []string{"3000", "--json"},
			owners:   []inspect.Owner{nodeOwner()},
			wantCode: exitOK,
			wantOut:  strings.Replace(nodeJSON, `"complete": true`, `"complete": false`, 1),
		},
		{
			name:     "no match json",
			args:     []string{"3000", "--json"},
			wantCode: exitNoMatch,
			wantOut: `{
  "schema": 1,
  "query": {
    "port": 3000,
    "protocol": ""
  },
  "complete": false,
  "owners": []
}
`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeInspector{result: inspect.Result{Owners: tt.owners}}
			var stdout, stderr bytes.Buffer

			code := Run(t.Context(), tt.args, &stdout, &stderr, Deps{Inspector: fake})

			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d", code, tt.wantCode)
			}
			if got := stdout.String(); got != tt.wantOut {
				t.Errorf("stdout mismatch\n got:\n%s\nwant:\n%s", got, tt.wantOut)
			}
			if stderr.Len() != 0 {
				t.Errorf("stderr = %q, want empty", stderr.String())
			}
		})
	}
}
