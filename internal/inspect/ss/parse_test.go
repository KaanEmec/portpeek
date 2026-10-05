package ss

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/kaanemec/portpeek/internal/inspect"
)

// Every fixture except no_users.txt and empty.txt was recorded as root in a
// golang:1.27 (Debian) container with iproute2 6.15.0, from a Go program
// named "sockets" (PID 1005 in the first recording, 945 in the second) and a
// forked child named "exe" (PID 1013). no_users.txt is the same socket as
// tcp4_listener.txt seen by an unprivileged user.

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	return data
}

// proc is the Process built for a fixture row before enrichment.
func proc(pid int, name string) inspect.Process {
	return inspect.Process{PID: pid, Name: name, Unavailable: map[inspect.Field]string{}}
}

func TestParseRows(t *testing.T) {
	t.Parallel()

	t.Run("established rows and users", func(t *testing.T) {
		t.Parallel()
		got, err := parseRows(readFixture(t, "established_mixed.txt"), "")
		if err != nil {
			t.Fatalf("parseRows: %v", err)
		}
		want := []row{
			{
				protocol: inspect.TCP,
				state:    "LISTEN",
				local:    "127.0.0.1:48123",
				peer:     "0.0.0.0:*",
				users:    []processRef{{name: "sockets", pid: 1005}},
			},
			{
				protocol: inspect.TCP,
				state:    "ESTAB",
				local:    "127.0.0.1:48123",
				peer:     "127.0.0.1:60166",
				users:    []processRef{{name: "sockets", pid: 1005}},
			},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("parseRows =\n%+v\nwant\n%+v", got, want)
		}
	})

	t.Run("several processes in one users list", func(t *testing.T) {
		t.Parallel()
		got, err := parseRows(readFixture(t, "two_pids.txt"), "")
		if err != nil {
			t.Fatalf("parseRows: %v", err)
		}
		want := []processRef{{name: "exe", pid: 1013}, {name: "sockets", pid: 1005}, {name: "sockets", pid: 1005}}
		if len(got) != 1 || !reflect.DeepEqual(got[0].users, want) {
			t.Errorf("parseRows = %+v, want one row with users %+v", got, want)
		}
	})

	t.Run("rows without users", func(t *testing.T) {
		t.Parallel()
		got, err := parseRows(readFixture(t, "no_users.txt"), "")
		if err != nil {
			t.Fatalf("parseRows: %v", err)
		}
		if len(got) != 2 || len(got[0].users) != 0 || got[0].users == nil {
			t.Errorf("parseRows = %+v, want two rows with empty non-nil users", got)
		}
	})

	t.Run("no netid column for a single protocol", func(t *testing.T) {
		t.Parallel()
		got, err := parseRows(readFixture(t, "tcp_only_no_netid.txt"), inspect.TCP)
		if err != nil {
			t.Fatalf("parseRows: %v", err)
		}
		want := []row{{
			protocol: inspect.TCP,
			state:    "LISTEN",
			local:    "*:48128",
			peer:     "*:*",
			users:    []processRef{{name: "sockets", pid: 945}},
		}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("parseRows = %+v, want %+v", got, want)
		}
	})

	t.Run("process name with spaces", func(t *testing.T) {
		t.Parallel()
		in := `tcp LISTEN 0 128 127.0.0.1:3000 0.0.0.0:* users:(("Web Content",pid=7,fd=3))` + "\n"
		got, err := parseRows([]byte(in), "")
		if err != nil {
			t.Fatalf("parseRows: %v", err)
		}
		if len(got) != 1 || !reflect.DeepEqual(got[0].users, []processRef{{name: "Web Content", pid: 7}}) {
			t.Errorf("parseRows = %+v", got)
		}
	})

	t.Run("empty output", func(t *testing.T) {
		t.Parallel()
		got, err := parseRows(readFixture(t, "empty.txt"), "")
		if err != nil || got == nil || len(got) != 0 {
			t.Errorf("parseRows(empty) = %#v, %v; want empty non-nil, nil", got, err)
		}
	})

	malformed := []struct {
		name     string
		in       string
		fallback inspect.Protocol
	}{
		{name: "too few columns", in: "tcp LISTEN 0 128 127.0.0.1:3000\n"},
		{name: "no netid and no fallback", in: "LISTEN 0 128 127.0.0.1:3000 0.0.0.0:*\n"},
		{name: "unknown process column", in: "tcp LISTEN 0 128 *:3000 *:* timer:(keepalive)\n"},
		{name: "non-numeric pid", in: `tcp LISTEN 0 128 *:3000 *:* users:(("a",pid=x,fd=3))` + "\n"},
		{name: "zero pid", in: `tcp LISTEN 0 128 *:3000 *:* users:(("a",pid=0,fd=3))` + "\n"},
		{name: "entry without pid", in: `tcp LISTEN 0 128 *:3000 *:* users:(("a",fd=3))` + "\n"},
		{name: "unterminated entry", in: `tcp LISTEN 0 128 *:3000 *:* users:(("a",pid=3)`},
	}
	for _, tt := range malformed {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got, err := parseRows([]byte(tt.in), tt.fallback); err == nil {
				t.Errorf("parseRows(%q) = %+v, expected error", tt.in, got)
			}
		})
	}
}

func TestBuildOwners(t *testing.T) {
	t.Parallel()

	sock := func(proto inspect.Protocol, family inspect.Family, addr string, port int, state string) inspect.Socket {
		return inspect.Socket{Protocol: proto, Family: family, Address: addr, Port: port, State: state}
	}
	unknown := proc(unknownPID, "")
	for _, f := range []inspect.Field{
		inspect.FieldName,
		inspect.FieldUser,
		inspect.FieldCommand,
		inspect.FieldWorkingDir,
	} {
		unknown.MarkUnavailable(f, reasonHidden)
	}

	tests := []struct {
		name     string
		fixture  string
		query    inspect.Query
		fallback inspect.Protocol
		want     []inspect.Owner
	}{
		{
			name:    "single ipv4 tcp listener",
			fixture: "tcp4_listener.txt",
			query:   inspect.Query{Port: 48123},
			want: []inspect.Owner{{
				Process: proc(1005, "sockets"),
				Sockets: []inspect.Socket{sock(inspect.TCP, inspect.IPv4, "127.0.0.1", 48123, inspect.StateListen)},
			}},
		},
		{
			name:    "ipv6 and ipv4 wildcard listeners grouped under one pid",
			fixture: "dual_stack_same_pid.txt",
			query:   inspect.Query{Port: 48124},
			want: []inspect.Owner{{
				Process: proc(1005, "sockets"),
				Sockets: []inspect.Socket{
					sock(inspect.TCP, inspect.IPv4, "*", 48124, inspect.StateListen),
					sock(inspect.TCP, inspect.IPv6, "*", 48124, inspect.StateListen),
				},
			}},
		},
		{
			name:    "dual stack star wildcard is ipv6",
			fixture: "dual_stack_wildcard.txt",
			query:   inspect.Query{Port: 48128},
			want: []inspect.Owner{{
				Process: proc(945, "sockets"),
				Sockets: []inspect.Socket{
					sock(inspect.TCP, inspect.IPv6, "*", 48128, inspect.StateListen),
					sock(inspect.UDP, inspect.IPv6, "*", 48128, inspect.StateBound),
				},
			}},
		},
		{
			name:    "udp unconnected sockets are bound",
			fixture: "udp_unconn.txt",
			query:   inspect.Query{Port: 48125},
			want: []inspect.Owner{{
				Process: proc(1005, "sockets"),
				Sockets: []inspect.Socket{
					sock(inspect.UDP, inspect.IPv4, "*", 48125, inspect.StateBound),
					sock(inspect.UDP, inspect.IPv6, "::1", 48125, inspect.StateBound),
				},
			}},
		},
		{
			name:    "udp sockets excluded by tcp query",
			fixture: "udp_unconn.txt",
			query:   inspect.Query{Port: 48125, Protocol: inspect.TCP},
			want:    []inspect.Owner{},
		},
		{
			name:    "tcp bound without listen is an owner",
			fixture: "tcp_bound_not_listening.txt",
			query:   inspect.Query{Port: 48132},
			want: []inspect.Owner{{
				Process: proc(945, "sockets"),
				Sockets: []inspect.Socket{sock(inspect.TCP, inspect.IPv4, "127.0.0.1", 48132, inspect.StateBound)},
			}},
		},
		{
			name:    "established connections filtered out",
			fixture: "established_mixed.txt",
			query:   inspect.Query{Port: 48123},
			want: []inspect.Owner{{
				Process: proc(1005, "sockets"),
				Sockets: []inspect.Socket{sock(inspect.TCP, inspect.IPv4, "127.0.0.1", 48123, inspect.StateListen)},
			}},
		},
		{
			name:    "two pids in one users list sorted by pid",
			fixture: "two_pids.txt",
			query:   inspect.Query{Port: 48126},
			want: []inspect.Owner{
				{
					Process: proc(1005, "sockets"),
					Sockets: []inspect.Socket{sock(inspect.TCP, inspect.IPv4, "127.0.0.1", 48126, inspect.StateListen)},
				},
				{
					Process: proc(1013, "exe"),
					Sockets: []inspect.Socket{sock(inspect.TCP, inspect.IPv4, "127.0.0.1", 48126, inspect.StateListen)},
				},
			},
		},
		{
			name:    "rows without users become the unknown owner",
			fixture: "no_users.txt",
			query:   inspect.Query{Port: 48123},
			want: []inspect.Owner{{
				Process: unknown,
				Sockets: []inspect.Socket{sock(inspect.TCP, inspect.IPv4, "127.0.0.1", 48123, inspect.StateListen)},
			}},
		},
		{
			name:    "link-local addresses keep their interface",
			fixture: "link_local.txt",
			query:   inspect.Query{Port: 48127},
			want: []inspect.Owner{{
				Process: proc(945, "sockets"),
				Sockets: []inspect.Socket{
					sock(inspect.TCP, inspect.IPv6, "fe80::1234%eth0", 48127, inspect.StateListen),
					sock(inspect.TCP, inspect.IPv6, "fe80::1234%lo", 48127, inspect.StateListen),
					sock(inspect.UDP, inspect.IPv6, "fe80::1234%eth0", 48127, inspect.StateBound),
					sock(inspect.UDP, inspect.IPv6, "fe80::1234%lo", 48127, inspect.StateBound),
				},
			}},
		},
		{
			name:    "wildcards bound to a device keep the device",
			fixture: "bound_to_device.txt",
			query:   inspect.Query{Port: 48129},
			want: []inspect.Owner{{
				Process: proc(945, "sockets"),
				Sockets: []inspect.Socket{
					sock(inspect.TCP, inspect.IPv4, "*%eth0", 48129, inspect.StateListen),
					sock(inspect.TCP, inspect.IPv6, "*%eth0", 48129, inspect.StateListen),
				},
			}},
		},
		{
			name:     "rows without netid use the query protocol",
			fixture:  "tcp_only_no_netid.txt",
			query:    inspect.Query{Port: 48128, Protocol: inspect.TCP},
			fallback: inspect.TCP,
			want: []inspect.Owner{{
				Process: proc(945, "sockets"),
				Sockets: []inspect.Socket{sock(inspect.TCP, inspect.IPv6, "*", 48128, inspect.StateListen)},
			}},
		},
		{
			name:    "other port yields no owners",
			fixture: "tcp4_listener.txt",
			query:   inspect.Query{Port: 48124},
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
			rows, err := parseRows(readFixture(t, tt.fixture), tt.fallback)
			if err != nil {
				t.Fatalf("parseRows: %v", err)
			}
			got := buildOwners(rows, tt.query)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("buildOwners =\n%+v\nwant\n%+v", got, tt.want)
			}
		})
	}
}

func TestBuildOwners_UnknownOwnerLast(t *testing.T) {
	t.Parallel()
	in := "tcp LISTEN 0 128 0.0.0.0:3000 0.0.0.0:*\n" +
		`tcp LISTEN 0 128 [::]:3000 [::]:* users:(("b",pid=20,fd=3))` + "\n" +
		`tcp LISTEN 0 128 127.0.0.1:3000 0.0.0.0:* users:(("a",pid=10,fd=3))` + "\n" +
		"tcp LISTEN 0 128 [::1]:3000 [::]:*\n"
	rows, err := parseRows([]byte(in), "")
	if err != nil {
		t.Fatalf("parseRows: %v", err)
	}
	got := buildOwners(rows, inspect.Query{Port: 3000})
	pids := []int{}
	for _, o := range got {
		pids = append(pids, o.Process.PID)
	}
	if !reflect.DeepEqual(pids, []int{10, 20, unknownPID}) {
		t.Fatalf("owner pids = %v, want [10 20 0]", pids)
	}
	if n := len(got[2].Sockets); n != 2 {
		t.Errorf("unknown owner sockets = %+v, want both unattributed sockets", got[2].Sockets)
	}
}

func TestIsWildcardPeer(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   string
		want bool
	}{
		{in: "0.0.0.0:*", want: true},
		{in: "[::]:*", want: true},
		{in: "*:*", want: true},
		{in: "[::]%eth0:*", want: true},
		{in: "127.0.0.1:60166"},
		{in: "[::1]:443"},
		{in: "127.0.0.1:*"},
		{in: ""},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			if got := isWildcardPeer(tt.in); got != tt.want {
				t.Errorf("isWildcardPeer(%q) = %t, want %t", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseLocal(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in         string
		wantAddr   string
		wantFamily inspect.Family
		wantPort   int
		wantOK     bool
	}{
		{in: "127.0.0.1:3000", wantAddr: "127.0.0.1", wantFamily: inspect.IPv4, wantPort: 3000, wantOK: true},
		{in: "0.0.0.0:3000", wantAddr: "*", wantFamily: inspect.IPv4, wantPort: 3000, wantOK: true},
		{in: "*:3000", wantAddr: "*", wantFamily: inspect.IPv6, wantPort: 3000, wantOK: true},
		{in: "[::1]:3000", wantAddr: "::1", wantFamily: inspect.IPv6, wantPort: 3000, wantOK: true},
		{in: "[::]:3000", wantAddr: "*", wantFamily: inspect.IPv6, wantPort: 3000, wantOK: true},
		{in: "[fe80::1]%eth0:3000", wantAddr: "fe80::1%eth0", wantFamily: inspect.IPv6, wantPort: 3000, wantOK: true},
		{in: "[fe80::1%eth0]:3000", wantAddr: "fe80::1%eth0", wantFamily: inspect.IPv6, wantPort: 3000, wantOK: true},
		{in: "*%eth0:3000", wantAddr: "*%eth0", wantFamily: inspect.IPv6, wantPort: 3000, wantOK: true},
		{in: "0.0.0.0%eth0:3000", wantAddr: "*%eth0", wantFamily: inspect.IPv4, wantPort: 3000, wantOK: true},
		{in: "10.0.0.5%eth0:3000", wantAddr: "10.0.0.5%eth0", wantFamily: inspect.IPv4, wantPort: 3000, wantOK: true},
		{
			in:         "[::ffff:127.0.0.1]:3000",
			wantAddr:   "::ffff:127.0.0.1",
			wantFamily: inspect.IPv6,
			wantPort:   3000,
			wantOK:     true,
		},
		{in: "127.0.0.1"},
		{in: "127.0.0.1:*"},
		{in: "127.0.0.1:70000"},
		{in: "[::1:3000"},
		{in: "[::1]eth0:3000"},
		{in: "localhost:3000"},
		{in: ":3000"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			addr, family, port, ok := parseLocal(tt.in)
			if addr != tt.wantAddr || family != tt.wantFamily || port != tt.wantPort || ok != tt.wantOK {
				t.Errorf("parseLocal(%q) = %q, %q, %d, %t; want %q, %q, %d, %t",
					tt.in, addr, family, port, ok, tt.wantAddr, tt.wantFamily, tt.wantPort, tt.wantOK)
			}
		})
	}
}
