package lsof

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/kaanemec/portpeek/internal/inspect"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	return data
}

func proc(pid int, name, user string) inspect.Process {
	return inspect.Process{PID: pid, Name: name, User: user, Unavailable: map[inspect.Field]string{}}
}

func TestParseRecords(t *testing.T) {
	t.Parallel()

	t.Run("nul terminated fields", func(t *testing.T) {
		t.Parallel()
		got, err := parseRecords(readFixture(t, "tcp4_listener.txt"))
		if err != nil {
			t.Fatalf("parseRecords: %v", err)
		}
		want := []processRecord{{
			pid:  42148,
			name: "Python",
			user: "kaanemec",
			files: []fileRecord{{
				fd:       "3",
				family:   "IPv4",
				protocol: "TCP",
				name:     "127.0.0.1:48123",
				state:    "LISTEN",
			}},
		}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("parseRecords = %+v, want %+v", got, want)
		}
	})

	t.Run("newline terminated fields", func(t *testing.T) {
		t.Parallel()
		got, err := parseRecords([]byte("p42674\nfcwd\nn/Users/me/my project\n"))
		if err != nil {
			t.Fatalf("parseRecords: %v", err)
		}
		want := []processRecord{{pid: 42674, files: []fileRecord{{fd: "cwd", name: "/Users/me/my project"}}}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("parseRecords = %+v, want %+v", got, want)
		}
	})

	t.Run("empty output", func(t *testing.T) {
		t.Parallel()
		got, err := parseRecords(nil)
		if err != nil || len(got) != 0 {
			t.Errorf("parseRecords(nil) = %+v, %v; want no records, nil", got, err)
		}
	})

	malformed := []struct {
		name string
		in   string
	}{
		{name: "field before process", in: "f3\x00tIPv4\x00\n"},
		{name: "non-numeric pid", in: "pabc\x00\n"},
		{name: "zero pid", in: "p0\x00\n"},
	}
	for _, tt := range malformed {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := parseRecords([]byte(tt.in)); err == nil {
				t.Errorf("parseRecords(%q): expected error", tt.in)
			}
		})
	}
}

func TestBuildOwners(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		fixture string
		query   inspect.Query
		want    []inspect.Owner
	}{
		{
			name:    "single ipv4 tcp listener",
			fixture: "tcp4_listener.txt",
			query:   inspect.Query{Port: 48123},
			want: []inspect.Owner{{
				Process: proc(42148, "Python", "kaanemec"),
				Sockets: []inspect.Socket{
					{Protocol: inspect.TCP, Family: inspect.IPv4, Address: "127.0.0.1", Port: 48123, State: "LISTEN"},
				},
			}},
		},
		{
			name:    "ipv6 and ipv4 wildcard listeners grouped under one pid",
			fixture: "dual_stack_same_pid.txt",
			query:   inspect.Query{Port: 48124},
			want: []inspect.Owner{{
				Process: proc(1007, "ControlCenter", "kaanemec"),
				Sockets: []inspect.Socket{
					{Protocol: inspect.TCP, Family: inspect.IPv4, Address: "*", Port: 48124, State: "LISTEN"},
					{Protocol: inspect.TCP, Family: inspect.IPv6, Address: "*", Port: 48124, State: "LISTEN"},
				},
			}},
		},
		{
			name:    "udp bound sockets kept and connected udp dropped",
			fixture: "udp_bound.txt",
			query:   inspect.Query{Port: 48125},
			want: []inspect.Owner{{
				Process: proc(501, "mDNSResponder", "_mdnsresponder"),
				Sockets: []inspect.Socket{
					{Protocol: inspect.UDP, Family: inspect.IPv4, Address: "*", Port: 48125},
					{Protocol: inspect.UDP, Family: inspect.IPv6, Address: "fe80::1%en0", Port: 48125},
				},
			}},
		},
		{
			name:    "udp sockets excluded by tcp query",
			fixture: "udp_bound.txt",
			query:   inspect.Query{Port: 48125, Protocol: inspect.TCP},
			want:    []inspect.Owner{},
		},
		{
			name:    "established connections filtered out",
			fixture: "established_mixed.txt",
			query:   inspect.Query{Port: 48123},
			want: []inspect.Owner{{
				Process: proc(42148, "Python", "kaanemec"),
				Sockets: []inspect.Socket{
					{Protocol: inspect.TCP, Family: inspect.IPv4, Address: "127.0.0.1", Port: 48123, State: "LISTEN"},
				},
			}},
		},
		{
			name:    "two pids on the same port sorted by pid",
			fixture: "two_pids.txt",
			query:   inspect.Query{Port: 8080},
			want: []inspect.Owner{
				{
					Process: proc(402, "nginx", "_www"),
					Sockets: []inspect.Socket{
						{Protocol: inspect.TCP, Family: inspect.IPv4, Address: "*", Port: 8080, State: "LISTEN"},
						{Protocol: inspect.TCP, Family: inspect.IPv6, Address: "::1", Port: 8080, State: "LISTEN"},
					},
				},
				{
					Process: proc(700, "nginx", "root"),
					Sockets: []inspect.Socket{
						{Protocol: inspect.TCP, Family: inspect.IPv4, Address: "*", Port: 8080, State: "LISTEN"},
					},
				},
			},
		},
		{
			name:    "only remote port matched yields no owners",
			fixture: "remote_port_only.txt",
			query:   inspect.Query{Port: 443},
			want:    []inspect.Owner{},
		},
		{
			name:    "empty output yields no owners",
			fixture: "empty.txt",
			query:   inspect.Query{Port: 48123},
			want:    []inspect.Owner{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			records, err := parseRecords(readFixture(t, tt.fixture))
			if err != nil {
				t.Fatalf("parseRecords: %v", err)
			}
			got := buildOwners(records, tt.query)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("buildOwners =\n%+v\nwant\n%+v", got, tt.want)
			}
		})
	}
}

func TestBuildOwners_DuplicateDescriptors(t *testing.T) {
	t.Parallel()
	in := "p10\x00cnode\x00Lme\x00\n" +
		"f3\x00tIPv4\x00PTCP\x00n*:3000\x00TST=LISTEN\x00\n" +
		"f7\x00tIPv4\x00PTCP\x00n*:3000\x00TST=LISTEN\x00\n"
	records, err := parseRecords([]byte(in))
	if err != nil {
		t.Fatalf("parseRecords: %v", err)
	}
	got := buildOwners(records, inspect.Query{Port: 3000})
	if len(got) != 1 || len(got[0].Sockets) != 1 {
		t.Errorf("buildOwners = %+v, want one owner with one socket", got)
	}
}

func TestBuildOwners_MissingNameAndUser(t *testing.T) {
	t.Parallel()
	in := "p10\x00\nf3\x00tIPv4\x00PTCP\x00n*:3000\x00TST=LISTEN\x00\n"
	records, err := parseRecords([]byte(in))
	if err != nil {
		t.Fatalf("parseRecords: %v", err)
	}
	got := buildOwners(records, inspect.Query{Port: 3000})
	if len(got) != 1 {
		t.Fatalf("buildOwners = %+v, want one owner", got)
	}
	for _, f := range []inspect.Field{inspect.FieldName, inspect.FieldUser} {
		if got[0].Process.Unavailable[f] == "" {
			t.Errorf("field %q not marked unavailable", f)
		}
	}
}

func TestSplitAddress(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in       string
		wantHost string
		wantPort int
		wantErr  bool
	}{
		{in: "127.0.0.1:3000", wantHost: "127.0.0.1", wantPort: 3000},
		{in: "*:3000", wantHost: "*", wantPort: 3000},
		{in: "[::1]:3000", wantHost: "::1", wantPort: 3000},
		{in: "[fe80::1%en0]:3000", wantHost: "fe80::1%en0", wantPort: 3000},
		{in: "[::]:53", wantHost: "::", wantPort: 53},
		{in: "*:*", wantErr: true},
		{in: "127.0.0.1", wantErr: true},
		{in: ":3000", wantErr: true},
		{in: "[::1:3000", wantErr: true},
		{in: "*:70000", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			host, port, err := splitAddress(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Errorf("splitAddress(%q) = %q, %d; want error", tt.in, host, port)
				}
				return
			}
			if err != nil || host != tt.wantHost || port != tt.wantPort {
				t.Errorf("splitAddress(%q) = %q, %d, %v; want %q, %d",
					tt.in, host, port, err, tt.wantHost, tt.wantPort)
			}
		})
	}

	t.Run("wildcard port reports errNoPort", func(t *testing.T) {
		t.Parallel()
		if _, _, err := splitAddress("*:*"); !errors.Is(err, errNoPort) {
			t.Errorf("splitAddress(\"*:*\") error = %v, want errNoPort", err)
		}
	})
}

func TestWorkingDir(t *testing.T) {
	t.Parallel()
	records, err := parseRecords([]byte("p1\nfcwd\nn/tmp/project\n"))
	if err != nil {
		t.Fatalf("parseRecords: %v", err)
	}
	if got := workingDir(records); got != "/tmp/project" {
		t.Errorf("workingDir = %q, want /tmp/project", got)
	}
	if got := workingDir(nil); got != "" {
		t.Errorf("workingDir(nil) = %q, want empty", got)
	}
}
