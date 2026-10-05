package lsof

import (
	"fmt"
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

// proc is the Process built for a fixture record. Every fixture was recorded
// from Python processes run by user kaanemec.
func proc(pid int) inspect.Process {
	return inspect.Process{PID: pid, Name: "Python", User: "kaanemec", Unavailable: map[inspect.Field]string{}}
}

// stubInterfaceNames replaces the interface lookup for the rest of the test.
// Callers must not run in parallel.
func stubInterfaceNames(t *testing.T, names map[int]string) {
	t.Helper()
	old := interfaceName
	interfaceName = func(index int) (string, error) {
		if name, ok := names[index]; ok {
			return name, nil
		}
		return "", fmt.Errorf("no interface with index %d", index)
	}
	t.Cleanup(func() { interfaceName = old })
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
			pid:  47885,
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

	listen := func(family inspect.Family, addr string, port int) inspect.Socket {
		return inspect.Socket{Protocol: inspect.TCP, Family: family, Address: addr, Port: port, State: inspect.StateListen}
	}
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
				Process: proc(47885),
				Sockets: []inspect.Socket{listen(inspect.IPv4, "127.0.0.1", 48123)},
			}},
		},
		{
			name:    "ipv6 and ipv4 wildcard listeners grouped under one pid",
			fixture: "dual_stack_same_pid.txt",
			query:   inspect.Query{Port: 48124},
			want: []inspect.Owner{{
				Process: proc(47885),
				Sockets: []inspect.Socket{
					listen(inspect.IPv4, "*", 48124),
					listen(inspect.IPv6, "*", 48124),
				},
			}},
		},
		{
			name:    "established connections filtered out",
			fixture: "established_mixed.txt",
			query:   inspect.Query{Port: 48123},
			want: []inspect.Owner{{
				Process: proc(47885),
				Sockets: []inspect.Socket{listen(inspect.IPv4, "127.0.0.1", 48123)},
			}},
		},
		{
			name:    "two pids on the same port sorted by pid",
			fixture: "two_pids.txt",
			query:   inspect.Query{Port: 48126},
			want: []inspect.Owner{
				{
					Process: proc(47907),
					Sockets: []inspect.Socket{
						listen(inspect.IPv4, "*", 48126),
						listen(inspect.IPv6, "::1", 48126),
					},
				},
				{
					Process: proc(47908),
					Sockets: []inspect.Socket{listen(inspect.IPv4, "*", 48126)},
				},
			},
		},
		{
			name:    "tcp bound without listen is an owner",
			fixture: "tcp_bound_not_listening.txt",
			query:   inspect.Query{Port: 48128},
			want: []inspect.Owner{{
				Process: proc(47885),
				Sockets: []inspect.Socket{
					{Protocol: inspect.TCP, Family: inspect.IPv4, Address: "127.0.0.1", Port: 48128, State: inspect.StateBound},
					{Protocol: inspect.TCP, Family: inspect.IPv6, Address: "*", Port: 48128, State: inspect.StateBound},
				},
			}},
		},
		{
			name:    "tcp bound without listen excluded by udp query",
			fixture: "tcp_bound_not_listening.txt",
			query:   inspect.Query{Port: 48128, Protocol: inspect.UDP},
			want:    []inspect.Owner{},
		},
		{
			name:    "only remote port matched yields no owners",
			fixture: "remote_port_only.txt",
			query:   inspect.Query{Port: 48127},
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

// TestBuildOwners_UDP is not parallel: its fixture has a link-local address,
// which needs the interface lookup stubbed.
func TestBuildOwners_UDP(t *testing.T) {
	stubInterfaceNames(t, map[int]string{1: "lo0"})

	tests := []struct {
		name  string
		query inspect.Query
		want  []inspect.Owner
	}{
		{
			name:  "udp bound sockets kept and connected udp dropped",
			query: inspect.Query{Port: 48125},
			want: []inspect.Owner{{
				Process: proc(47885),
				Sockets: []inspect.Socket{
					{Protocol: inspect.UDP, Family: inspect.IPv4, Address: "*", Port: 48125, State: inspect.StateBound},
					{Protocol: inspect.UDP, Family: inspect.IPv6, Address: "fe80::1%lo0", Port: 48125, State: inspect.StateBound},
				},
			}},
		},
		{
			name:  "udp sockets excluded by tcp query",
			query: inspect.Query{Port: 48125, Protocol: inspect.TCP},
			want:  []inspect.Owner{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			records, err := parseRecords(readFixture(t, "udp_bound.txt"))
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
		wantOK   bool
	}{
		{in: "127.0.0.1:3000", wantHost: "127.0.0.1", wantPort: 3000, wantOK: true},
		{in: "*:3000", wantHost: "*", wantPort: 3000, wantOK: true},
		{in: "[::1]:3000", wantHost: "::1", wantPort: 3000, wantOK: true},
		{in: "[fe80:1::1]:48125", wantHost: "fe80:1::1", wantPort: 48125, wantOK: true},
		{in: "[::]:53", wantHost: "::", wantPort: 53, wantOK: true},
		{in: "*:*"},
		{in: "127.0.0.1"},
		{in: ":3000"},
		{in: "[::1:3000"},
		{in: "*:70000"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			host, port, ok := splitAddress(tt.in)
			if host != tt.wantHost || port != tt.wantPort || ok != tt.wantOK {
				t.Errorf("splitAddress(%q) = %q, %d, %t; want %q, %d, %t",
					tt.in, host, port, ok, tt.wantHost, tt.wantPort, tt.wantOK)
			}
		})
	}
}

// TestUnpackScope is not parallel because it stubs the interface lookup.
func TestUnpackScope(t *testing.T) {
	stubInterfaceNames(t, map[int]string{1: "lo0", 4: "en0"})

	tests := []struct {
		in   string
		want string
	}{
		// The shape lsof prints for fe80::1%lo0 (scope id 1).
		{in: "fe80:1::1", want: "fe80::1%lo0"},
		{in: "fe80:4::aede:48ff:fe00:1122", want: "fe80::aede:48ff:fe00:1122%en0"},
		{in: "fe80:7::1", want: "fe80::1%7"},
		{in: "fe80::1", want: "fe80::1"},
		{in: "fe80::1%en0", want: "fe80::1%en0"},
		{in: "2001:db8::1", want: "2001:db8::1"},
		{in: "::1", want: "::1"},
		{in: "::ffff:169.254.1.1", want: "::ffff:169.254.1.1"},
		{in: "*", want: "*"},
	}
	for _, tt := range tests {
		if got := unpackScope(tt.in); got != tt.want {
			t.Errorf("unpackScope(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
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
