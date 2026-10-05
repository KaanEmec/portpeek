package netstat

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"testing"

	"github.com/kaanemec/portpeek/internal/inspect"
)

func TestInspector_enrich(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		resp            response
		missing         bool // powershell is not installed
		wantName        string
		wantCommand     string
		wantUnavailable map[inspect.Field]string
	}{
		{
			name:        "full details",
			resp:        response{stdout: nodeJSON + "\r\n"},
			wantName:    "node.exe",
			wantCommand: `"C:\Program Files\nodejs\node.exe" server.js`,
		},
		{
			name: "null command line falls back to executable path",
			resp: response{
				stdout: `{"Name":"svchost.exe","CommandLine":null,"ExecutablePath":"C:\\Windows\\System32\\svchost.exe"}`,
			},
			wantName:    "svchost.exe",
			wantCommand: `C:\Windows\System32\svchost.exe`,
		},
		{
			name:            "command line and path both null",
			resp:            response{stdout: `{"Name":"sqlservr.exe","CommandLine":null,"ExecutablePath":null}`},
			wantName:        "sqlservr.exe",
			wantUnavailable: map[inspect.Field]string{inspect.FieldCommand: reasonPrivileges},
		},
		{
			name:        "utf-8 byte order mark",
			resp:        response{stdout: "\xef\xbb\xbf" + nodeJSON},
			wantName:    "node.exe",
			wantCommand: `"C:\Program Files\nodejs\node.exe" server.js`,
		},
		{
			name: "empty name",
			resp: response{stdout: `{"Name":"","CommandLine":"x.exe","ExecutablePath":null}`},
			wantUnavailable: map[inspect.Field]string{
				inspect.FieldName: reasonUnknown,
			},
			wantCommand: "x.exe",
		},
		{
			name: "empty output means the process exited",
			resp: response{stdout: "\r\n"},
			wantUnavailable: map[inspect.Field]string{
				inspect.FieldName:    reasonExited,
				inspect.FieldCommand: reasonExited,
			},
		},
		{
			name: "empty output with an error on stderr",
			resp: response{stderr: "Get-CimInstance : Access denied\r\n"},
			wantUnavailable: map[inspect.Field]string{
				inspect.FieldName:    reasonUnknown,
				inspect.FieldCommand: reasonUnknown,
			},
		},
		{
			name: "non-zero exit",
			resp: response{code: 1, stdout: nodeJSON},
			wantUnavailable: map[inspect.Field]string{
				inspect.FieldName:    reasonUnknown,
				inspect.FieldCommand: reasonUnknown,
			},
		},
		{
			name: "invalid json",
			resp: response{stdout: "Name : node.exe\r\n"},
			wantUnavailable: map[inspect.Field]string{
				inspect.FieldName:    reasonUnknown,
				inspect.FieldCommand: reasonUnknown,
			},
		},
		{
			name: "command did not run",
			resp: response{code: -1, err: errors.New("signal: killed")},
			wantUnavailable: map[inspect.Field]string{
				inspect.FieldName:    reasonUnknown,
				inspect.FieldCommand: reasonUnknown,
			},
		},
		{
			name:    "powershell not installed",
			missing: true,
			wantUnavailable: map[inspect.Field]string{
				inspect.FieldName:    reasonUnknown,
				inspect.FieldCommand: reasonUnknown,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			responses := map[string]response{}
			if !tt.missing {
				responses[psKey(1234)] = tt.resp
			}
			i := NewWithRunner(newFakeRunner(responses))

			p := proc(1234)
			i.enrich(context.Background(), &p)

			want := proc(1234)
			want.Name = tt.wantName
			want.Command = tt.wantCommand
			for f, reason := range tt.wantUnavailable {
				want.MarkUnavailable(f, reason)
			}
			if !reflect.DeepEqual(p, want) {
				t.Errorf("enrich =\n%+v\nwant\n%+v", p, want)
			}
		})
	}
}

func TestInspector_Inspect_PowerShellMissing(t *testing.T) {
	t.Parallel()

	responses := netstatResponses(t, map[string]string{"TCP": string(readFixture(t, "tcp4_listener.txt"))})
	responses[psKey(1234)] = response{code: -1, err: &exec.Error{Name: "powershell", Err: exec.ErrNotFound}}

	got, err := NewWithRunner(newFakeRunner(responses)).Inspect(context.Background(), inspect.Query{Port: 3000})
	if err != nil {
		t.Fatalf("Inspect: %v, want enrichment failure to be recorded, not returned", err)
	}
	if len(got.Owners) != 1 || got.Owners[0].Process.PID != 1234 {
		t.Fatalf("Owners = %+v, want pid 1234", got.Owners)
	}
	if reason := got.Owners[0].Process.Unavailable[inspect.FieldName]; reason != reasonUnknown {
		t.Errorf("Unavailable[name] = %q, want %q", reason, reasonUnknown)
	}
}

func TestProcessArgs(t *testing.T) {
	t.Parallel()
	got := processArgs(1234)
	want := []string{
		"-NoProfile",
		"-NonInteractive",
		"-Command",
		"Get-CimInstance Win32_Process -Filter 'ProcessId = 1234' | " +
			"Select-Object Name,CommandLine,ExecutablePath | ConvertTo-Json -Compress",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("processArgs(1234) =\n%q\nwant\n%q", got, want)
	}
}
