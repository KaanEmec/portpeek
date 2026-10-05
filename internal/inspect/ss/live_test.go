//go:build linux

package ss

import (
	"context"
	"net"
	"os"
	"testing"
	"time"

	"github.com/kaanemec/portpeek/internal/inspect"
)

// TestInspector_Inspect_Live runs the real ss and reads the real /proc for
// sockets this test process opens. It is skipped with -short. Run it alone
// with: go test -run Live ./internal/inspect/ss
func TestInspector_Inspect_Live(t *testing.T) {
	if testing.Short() {
		t.Skip("live ss test skipped in -short mode")
	}

	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen tcp: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	t.Cleanup(func() { _ = pc.Close() })

	// A client connection to the listener. Its ephemeral port appears only
	// on connected sockets, so it has no owner.
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

	portTCP := ln.Addr().(*net.TCPAddr).Port
	portUDP := pc.LocalAddr().(*net.UDPAddr).Port
	tests := []struct {
		name  string
		query inspect.Query
		want  inspect.Socket
	}{
		{
			name:  "ipv4 tcp listener",
			query: inspect.Query{Port: portTCP},
			want: inspect.Socket{
				Protocol: inspect.TCP,
				Family:   inspect.IPv4,
				Address:  "127.0.0.1",
				Port:     portTCP,
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
				t.Errorf("Name, User = %q, %q; Unavailable = %v", p.Name, p.User, p.Unavailable)
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
