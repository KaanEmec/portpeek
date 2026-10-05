//go:build darwin

package lsof

import (
	"context"
	"net"
	"os"
	"testing"
	"time"

	"github.com/kaanemec/portpeek/internal/inspect"
)

// TestInspector_Inspect_Live runs the real lsof and ps against sockets this
// test process opens. Run with: go test -run Live ./internal/inspect/lsof
func TestInspector_Inspect_Live(t *testing.T) {
	if testing.Short() {
		t.Skip("live lsof test skipped in -short mode")
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen tcp: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	t.Cleanup(func() { _ = pc.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	tests := []struct {
		name  string
		query inspect.Query
		want  inspect.Socket
	}{
		{
			name:  "tcp listener",
			query: inspect.Query{Port: ln.Addr().(*net.TCPAddr).Port},
			want: inspect.Socket{
				Protocol: inspect.TCP,
				Family:   inspect.IPv4,
				Address:  "127.0.0.1",
				Port:     ln.Addr().(*net.TCPAddr).Port,
				State:    "LISTEN",
			},
		},
		{
			name:  "udp bound socket",
			query: inspect.Query{Port: pc.LocalAddr().(*net.UDPAddr).Port, Protocol: inspect.UDP},
			want: inspect.Socket{
				Protocol: inspect.UDP,
				Family:   inspect.IPv4,
				Address:  "127.0.0.1",
				Port:     pc.LocalAddr().(*net.UDPAddr).Port,
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
}
