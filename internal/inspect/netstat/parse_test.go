package netstat

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/kaanemec/portpeek/internal/inspect"
)

// The fixtures are not recordings: no Windows machine was available. They
// are built from the documented `netstat -a -n -o -p TABLE` layout (title,
// localized column header, CRLF line endings, PID last, UDP rows without a
// state column) and must be replaced by recordings once a Windows run
// confirms or corrects them. PID 1234 and 5678 are user processes, 1012 and
// 2424 unrelated services, 4 the System process.

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	return data
}

// proc is the Process built for an ordinary fixture PID before enrichment.
func proc(pid int) inspect.Process {
	return inspect.Process{
		PID: pid,
		Unavailable: map[inspect.Field]string{
			inspect.FieldUser:       reasonNotOnWindows,
			inspect.FieldWorkingDir: reasonNotOnWindows,
		},
	}
}

func TestParseRows(t *testing.T) {
	t.Parallel()

	t.Run("tcp rows with states", func(t *testing.T) {
		t.Parallel()
		got, err := parseRows(readFixture(t, "established_mixed.txt"))
		if err != nil {
			t.Fatalf("parseRows: %v", err)
		}
		want := []row{
			{protocol: inspect.TCP, local: "127.0.0.1:3000", peer: "0.0.0.0:0", state: "LISTENING", pid: 1234},
			{protocol: inspect.TCP, local: "127.0.0.1:3000", peer: "127.0.0.1:52000", state: "ESTABLISHED", pid: 1234},
			{protocol: inspect.TCP, local: "127.0.0.1:3000", peer: "127.0.0.1:51999", state: "TIME_WAIT", pid: 0},
			{protocol: inspect.TCP, local: "127.0.0.1:52000", peer: "127.0.0.1:3000", state: "ESTABLISHED", pid: 5678},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("parseRows =\n%+v\nwant\n%+v", got, want)
		}
	})

	t.Run("udp rows without a state column", func(t *testing.T) {
		t.Parallel()
		got, err := parseRows(readFixture(t, "udpv6_loopback.txt"))
		if err != nil {
			t.Fatalf("parseRows: %v", err)
		}
		want := []row{
			{protocol: inspect.UDP, local: "[::1]:3000", peer: "*:*", pid: 1234},
			{protocol: inspect.UDP, local: "[::]:5353", peer: "*:*", pid: 2424},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("parseRows =\n%+v\nwant\n%+v", got, want)
		}
	})

	t.Run("localized header and LF line endings", func(t *testing.T) {
		t.Parallel()
		in := "\nAktive Verbindungen\n\n" +
			"  Proto  Lokale Adresse         Remoteadresse          Status           PID\n" +
			"  TCP    0.0.0.0:3000           0.0.0.0:0              ABHÖREN          1234\n"
		got, err := parseRows([]byte(in))
		if err != nil {
			t.Fatalf("parseRows: %v", err)
		}
		want := []row{{protocol: inspect.TCP, local: "0.0.0.0:3000", peer: "0.0.0.0:0", state: "ABHÖREN", pid: 1234}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("parseRows = %+v, want %+v", got, want)
		}
	})

	t.Run("state with spaces", func(t *testing.T) {
		t.Parallel()
		got, err := parseRows([]byte("  TCP    0.0.0.0:3000   0.0.0.0:0   EN ECOUTE   1234\r\n"))
		if err != nil {
			t.Fatalf("parseRows: %v", err)
		}
		if len(got) != 1 || got[0].state != "EN ECOUTE" || got[0].pid != 1234 {
			t.Errorf("parseRows = %+v", got)
		}
	})

	t.Run("header only", func(t *testing.T) {
		t.Parallel()
		got, err := parseRows(readFixture(t, "empty.txt"))
		if err != nil || got == nil || len(got) != 0 {
			t.Errorf("parseRows(header only) = %#v, %v; want empty non-nil, nil", got, err)
		}
	})

	t.Run("empty output", func(t *testing.T) {
		t.Parallel()
		got, err := parseRows(nil)
		if err != nil || got == nil || len(got) != 0 {
			t.Errorf("parseRows(nil) = %#v, %v; want empty non-nil, nil", got, err)
		}
	})

	malformed := []struct {
		name string
		in   string
	}{
		{name: "tcp too few columns", in: "  TCP    0.0.0.0:3000   0.0.0.0:0   1234\r\n"},
		{name: "tcp non-numeric pid", in: "  TCP    0.0.0.0:3000   0.0.0.0:0   LISTENING   x\r\n"},
		{name: "tcp negative pid", in: "  TCP    0.0.0.0:3000   0.0.0.0:0   LISTENING   -1\r\n"},
		{name: "udp too few columns", in: "  UDP    0.0.0.0:3000   1234\r\n"},
		{name: "udp too many columns", in: "  UDP    0.0.0.0:3000   *:*   LISTENING   1234\r\n"},
		{name: "udp non-numeric pid", in: "  UDP    0.0.0.0:3000   *:*   x\r\n"},
	}
	for _, tt := range malformed {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got, err := parseRows([]byte(tt.in)); err == nil {
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
	unknown := inspect.Process{PID: unknownPID, Unavailable: map[inspect.Field]string{}}
	for _, f := range []inspect.Field{
		inspect.FieldName,
		inspect.FieldUser,
		inspect.FieldCommand,
		inspect.FieldWorkingDir,
	} {
		unknown.MarkUnavailable(f, reasonNotReported)
	}
	system := proc(systemPID)
	system.Name = "System"
	system.MarkUnavailable(inspect.FieldCommand, reasonSystemProcess)

	tests := []struct {
		name     string
		fixtures []string
		query    inspect.Query
		want     []inspect.Owner
	}{
		{
			name:     "single ipv4 tcp listener",
			fixtures: []string{"tcp4_listener.txt"},
			query:    inspect.Query{Port: 3000},
			want: []inspect.Owner{{
				Process: proc(1234),
				Sockets: []inspect.Socket{sock(inspect.TCP, inspect.IPv4, "127.0.0.1", 3000, inspect.StateListen)},
			}},
		},
		{
			name:     "ipv6 and ipv4 wildcard listeners grouped under one pid",
			fixtures: []string{"dual_stack_tcp.txt", "dual_stack_tcpv6.txt"},
			query:    inspect.Query{Port: 3000},
			want: []inspect.Owner{{
				Process: proc(1234),
				Sockets: []inspect.Socket{
					sock(inspect.TCP, inspect.IPv4, "*", 3000, inspect.StateListen),
					sock(inspect.TCP, inspect.IPv6, "*", 3000, inspect.StateListen),
				},
			}},
		},
		{
			name:     "udp sockets are bound",
			fixtures: []string{"udp_bound.txt", "udpv6_loopback.txt"},
			query:    inspect.Query{Port: 3000},
			want: []inspect.Owner{{
				Process: proc(1234),
				Sockets: []inspect.Socket{
					sock(inspect.UDP, inspect.IPv4, "*", 3000, inspect.StateBound),
					sock(inspect.UDP, inspect.IPv6, "::1", 3000, inspect.StateBound),
				},
			}},
		},
		{
			name:     "udp sockets excluded by tcp query",
			fixtures: []string{"udp_bound.txt"},
			query:    inspect.Query{Port: 3000, Protocol: inspect.TCP},
			want:     []inspect.Owner{},
		},
		{
			name:     "tcp bound without listen is an owner",
			fixtures: []string{"tcp_bound_not_listening.txt"},
			query:    inspect.Query{Port: 3001},
			want: []inspect.Owner{{
				Process: proc(1234),
				Sockets: []inspect.Socket{sock(inspect.TCP, inspect.IPv4, "*", 3001, inspect.StateBound)},
			}},
		},
		{
			name:     "established and time-wait rows filtered out",
			fixtures: []string{"established_mixed.txt"},
			query:    inspect.Query{Port: 3000},
			want: []inspect.Owner{{
				Process: proc(1234),
				Sockets: []inspect.Socket{sock(inspect.TCP, inspect.IPv4, "127.0.0.1", 3000, inspect.StateListen)},
			}},
		},
		{
			name:     "client ephemeral port has no owner",
			fixtures: []string{"established_mixed.txt"},
			query:    inspect.Query{Port: 52000},
			want:     []inspect.Owner{},
		},
		{
			name:     "two pids on one port sorted by pid",
			fixtures: []string{"two_pids.txt"},
			query:    inspect.Query{Port: 3000},
			want: []inspect.Owner{
				{
					Process: proc(1234),
					Sockets: []inspect.Socket{sock(inspect.TCP, inspect.IPv4, "*", 3000, inspect.StateListen)},
				},
				{
					Process: proc(5678),
					Sockets: []inspect.Socket{sock(inspect.TCP, inspect.IPv4, "127.0.0.1", 3000, inspect.StateListen)},
				},
			},
		},
		{
			name:     "link-local addresses keep their zone",
			fixtures: []string{"link_local.txt"},
			query:    inspect.Query{Port: 3000},
			want: []inspect.Owner{{
				Process: proc(1234),
				Sockets: []inspect.Socket{
					sock(inspect.TCP, inspect.IPv6, "::1", 3000, inspect.StateListen),
					sock(inspect.TCP, inspect.IPv6, "fe80::1%12", 3000, inspect.StateListen),
				},
			}},
		},
		{
			name:     "pid 0 rows become the unknown owner, listed last",
			fixtures: []string{"pid_zero.txt"},
			query:    inspect.Query{Port: 3000},
			want: []inspect.Owner{
				{
					Process: proc(1234),
					Sockets: []inspect.Socket{sock(inspect.TCP, inspect.IPv4, "127.0.0.1", 3000, inspect.StateListen)},
				},
				{
					Process: unknown,
					Sockets: []inspect.Socket{sock(inspect.TCP, inspect.IPv4, "*", 3000, inspect.StateListen)},
				},
			},
		},
		{
			name:     "pid 4 is the System process",
			fixtures: []string{"system_pid.txt"},
			query:    inspect.Query{Port: 80},
			want: []inspect.Owner{{
				Process: system,
				Sockets: []inspect.Socket{sock(inspect.TCP, inspect.IPv4, "*", 80, inspect.StateListen)},
			}},
		},
		{
			name:     "duplicate rows across tables collapse",
			fixtures: []string{"tcp4_listener.txt", "tcp4_listener.txt"},
			query:    inspect.Query{Port: 3000},
			want: []inspect.Owner{{
				Process: proc(1234),
				Sockets: []inspect.Socket{sock(inspect.TCP, inspect.IPv4, "127.0.0.1", 3000, inspect.StateListen)},
			}},
		},
		{
			name:     "other port yields no owners",
			fixtures: []string{"tcp4_listener.txt"},
			query:    inspect.Query{Port: 3001},
			want:     []inspect.Owner{},
		},
		{
			name:     "header-only output yields no owners",
			fixtures: []string{"empty.txt"},
			query:    inspect.Query{Port: 3000},
			want:     []inspect.Owner{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rows := []row{}
			for _, f := range tt.fixtures {
				parsed, err := parseRows(readFixture(t, f))
				if err != nil {
					t.Fatalf("parseRows(%s): %v", f, err)
				}
				rows = append(rows, parsed...)
			}
			got := buildOwners(rows, tt.query)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("buildOwners =\n%+v\nwant\n%+v", got, tt.want)
			}
		})
	}
}

func TestOwnedSocket_States(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		r         row
		wantState string
		wantOK    bool
	}{
		{
			name:      "tcp listening",
			r:         row{protocol: inspect.TCP, local: "0.0.0.0:3000", peer: "0.0.0.0:0", state: "LISTENING", pid: 1},
			wantState: inspect.StateListen,
			wantOK:    true,
		},
		{
			name:      "tcp bound",
			r:         row{protocol: inspect.TCP, local: "[::]:3000", peer: "[::]:0", state: "BOUND", pid: 1},
			wantState: inspect.StateBound,
			wantOK:    true,
		},
		{
			name:      "udp",
			r:         row{protocol: inspect.UDP, local: "0.0.0.0:3000", peer: "*:*", pid: 1},
			wantState: inspect.StateBound,
			wantOK:    true,
		},
		{
			name: "tcp closed without peer",
			r:    row{protocol: inspect.TCP, local: "0.0.0.0:3000", peer: "0.0.0.0:0", state: "CLOSED", pid: 1},
		},
		{
			name: "tcp unknown localized state",
			r:    row{protocol: inspect.TCP, local: "0.0.0.0:3000", peer: "0.0.0.0:0", state: "ABHÖREN", pid: 1},
		},
		{
			name: "tcp syn sent",
			r:    row{protocol: inspect.TCP, local: "10.0.0.2:3000", peer: "10.0.0.9:443", state: "SYN_SENT", pid: 1},
		},
		{
			name: "tcp close wait",
			r:    row{protocol: inspect.TCP, local: "127.0.0.1:3000", peer: "127.0.0.1:5000", state: "CLOSE_WAIT", pid: 1},
		},
		{
			name: "udp with a peer",
			r:    row{protocol: inspect.UDP, local: "0.0.0.0:3000", peer: "10.0.0.9:53", pid: 1},
		},
		{
			name: "malformed local address",
			r:    row{protocol: inspect.TCP, local: "nonsense:3000", peer: "0.0.0.0:0", state: "LISTENING", pid: 1},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := ownedSocket(tt.r, inspect.Query{Port: 3000})
			if ok != tt.wantOK || got.State != tt.wantState {
				t.Errorf("ownedSocket(%+v) = %+v, %t; want state %q, %t", tt.r, got, ok, tt.wantState, tt.wantOK)
			}
		})
	}
}

func TestIsWildcardPeer(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   string
		want bool
	}{
		{in: "0.0.0.0:0", want: true},
		{in: "[::]:0", want: true},
		{in: "*:*", want: true},
		{in: "[::%12]:0", want: true},
		{in: "127.0.0.1:52000"},
		{in: "[::1]:443"},
		{in: "127.0.0.1:0"},
		{in: "0.0.0.0:80"},
		{in: "[::]0"},
		{in: "[:::0"},
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
		{in: "192.168.1.20:3000", wantAddr: "192.168.1.20", wantFamily: inspect.IPv4, wantPort: 3000, wantOK: true},
		{in: "[::1]:3000", wantAddr: "::1", wantFamily: inspect.IPv6, wantPort: 3000, wantOK: true},
		{in: "[::]:3000", wantAddr: "*", wantFamily: inspect.IPv6, wantPort: 3000, wantOK: true},
		{in: "[fe80::1%12]:3000", wantAddr: "fe80::1%12", wantFamily: inspect.IPv6, wantPort: 3000, wantOK: true},
		{
			in:         "[fe80::a00:27ff:fe4e:66a1%7]:65535",
			wantAddr:   "fe80::a00:27ff:fe4e:66a1%7",
			wantFamily: inspect.IPv6,
			wantPort:   65535,
			wantOK:     true,
		},
		{in: "127.0.0.1"},
		{in: "127.0.0.1:x"},
		{in: "127.0.0.1:70000"},
		{in: "[::1:3000"},
		{in: "[127.0.0.1]:3000"},
		{in: "::1:3000"},
		{in: "*:3000"},
		{in: "localhost:3000"},
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
