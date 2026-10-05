//go:build darwin

package lsof

import (
	"context"
	"net"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/kaanemec/portpeek/internal/inspect"
)

// TestInspector_Inspect_Live runs the real lsof and ps against sockets this
// test process opens. It is skipped with -short. Run it alone with:
// go test -run Live ./internal/inspect/lsof
func TestInspector_Inspect_Live(t *testing.T) {
	if testing.Short() {
		t.Skip("live lsof test skipped in -short mode")
	}

	ln4 := listenTCP(t, "tcp4", "127.0.0.1:0")
	ln6 := listenTCP(t, "tcp6", "[::1]:0")

	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	t.Cleanup(func() { _ = pc.Close() })

	// A client connection to the IPv4 listener. Its ephemeral port appears
	// only as the local end of the client socket and the remote end of the
	// accepted socket, so it has no owner.
	client, err := net.Dial("tcp4", ln4.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	accepted, err := ln4.Accept()
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	t.Cleanup(func() { _ = accepted.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	port4 := ln4.Addr().(*net.TCPAddr).Port
	port6 := ln6.Addr().(*net.TCPAddr).Port
	portUDP := pc.LocalAddr().(*net.UDPAddr).Port
	tests := []struct {
		name  string
		query inspect.Query
		want  inspect.Socket
	}{
		{
			name:  "ipv4 tcp listener",
			query: inspect.Query{Port: port4},
			want: inspect.Socket{
				Protocol: inspect.TCP,
				Family:   inspect.IPv4,
				Address:  "127.0.0.1",
				Port:     port4,
				State:    inspect.StateListen,
			},
		},
		{
			name:  "ipv6 tcp listener",
			query: inspect.Query{Port: port6, Protocol: inspect.TCP},
			want: inspect.Socket{
				Protocol: inspect.TCP,
				Family:   inspect.IPv6,
				Address:  "::1",
				Port:     port6,
				State:    inspect.StateListen,
			},
		},
		{
			name:  "udp bound socket",
			query: inspect.Query{Port: portUDP, Protocol: inspect.UDP},
			want: inspect.Socket{
				Protocol: inspect.UDP,
				Family:   inspect.IPv4,
				Address:  "127.0.0.1",
				Port:     portUDP,
				State:    inspect.StateBound,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := New().Inspect(ctx, tt.query)
			if err != nil {
				t.Fatalf("Inspect(%+v): %v", tt.query, err)
			}
			var self *inspect.Owner
			for i := range res.Owners {
				if res.Owners[i].Process.PID == os.Getpid() {
					self = &res.Owners[i]
				}
			}
			if self == nil {
				t.Fatalf("pid %d not among owners %+v", os.Getpid(), res.Owners)
			}
			if len(self.Sockets) != 1 || self.Sockets[0] != tt.want {
				t.Errorf("Sockets = %+v, want [%+v]", self.Sockets, tt.want)
			}
			p := self.Process
			if p.Command == "" {
				t.Errorf("Command empty, Unavailable = %v", p.Unavailable)
			}
			if p.WorkingDir == "" {
				t.Errorf("WorkingDir empty, Unavailable = %v", p.Unavailable)
			}
			if p.Name == "" || p.User == "" {
				t.Errorf("Name, User = %q, %q", p.Name, p.User)
			}
		})
	}

	t.Run("client ephemeral port has no owner", func(t *testing.T) {
		q := inspect.Query{Port: client.LocalAddr().(*net.TCPAddr).Port, Protocol: inspect.TCP}
		res, err := New().Inspect(ctx, q)
		if err != nil {
			t.Fatalf("Inspect(%+v): %v", q, err)
		}
		if len(res.Owners) != 0 {
			t.Errorf("Owners = %+v, want none", res.Owners)
		}
	})
}

// TestInspector_List_Live runs the real lsof listing against sockets this
// test process opens. It is skipped with -short.
func TestInspector_List_Live(t *testing.T) {
	if testing.Short() {
		t.Skip("live lsof test skipped in -short mode")
	}

	ln := listenTCP(t, "tcp4", "127.0.0.1:0")
	pc, err := net.ListenPacket("udp6", "[::1]:0")
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	t.Cleanup(func() { _ = pc.Close() })

	// A client connection to the listener: neither end may show up.
	client, err := net.Dial("tcp4", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	accepted, err := ln.Accept()
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	t.Cleanup(func() { _ = accepted.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	snap, err := New().List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if snap.Taken.IsZero() {
		t.Error("Taken is zero")
	}
	idx := slices.IndexFunc(snap.Owners, func(o inspect.Owner) bool { return o.Process.PID == os.Getpid() })
	if idx < 0 {
		t.Fatalf("pid %d not among %d owners", os.Getpid(), len(snap.Owners))
	}
	self := snap.Owners[idx]

	want := []inspect.Socket{
		{
			Protocol: inspect.TCP,
			Family:   inspect.IPv4,
			Address:  "127.0.0.1",
			Port:     ln.Addr().(*net.TCPAddr).Port,
			State:    inspect.StateListen,
		},
		{
			Protocol: inspect.UDP,
			Family:   inspect.IPv6,
			Address:  "::1",
			Port:     pc.LocalAddr().(*net.UDPAddr).Port,
			State:    inspect.StateBound,
		},
	}
	for _, s := range want {
		if !slices.Contains(self.Sockets, s) {
			t.Errorf("socket %+v missing from %+v", s, self.Sockets)
		}
	}
	p := self.Process
	if p.Name == "" || p.User == "" {
		t.Errorf("Name, User = %q, %q; Unavailable = %v", p.Name, p.User, p.Unavailable)
	}
	if p.Command != "" || p.WorkingDir != "" || len(p.Unavailable) != 0 {
		t.Errorf("Process = %+v, want no enrichment and nothing unavailable", p)
	}

	clientPort := client.LocalAddr().(*net.TCPAddr).Port
	for _, s := range self.Sockets {
		if s.Port == clientPort {
			t.Errorf("client ephemeral port %d listed as owned: %+v", clientPort, s)
		}
	}
}

func listenTCP(t *testing.T, network, addr string) net.Listener {
	t.Helper()
	ln, err := net.Listen(network, addr)
	if err != nil {
		t.Fatalf("listen %s %s: %v", network, addr, err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln
}
