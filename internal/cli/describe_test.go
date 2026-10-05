package cli

import (
	"slices"
	"testing"

	"github.com/kaanemec/portpeek/internal/inspect"
)

func TestCollapseBindings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		sockets []inspect.Socket
		want    []Binding
	}{
		{
			name:    "none",
			sockets: nil,
			want:    []Binding{},
		},
		{
			name: "families of one address collapse",
			sockets: []inspect.Socket{
				udpSocket(inspect.IPv4, "*", 5353),
				udpSocket(inspect.IPv6, "*", 5353),
			},
			want: []Binding{
				{Protocol: inspect.UDP, Address: "*", Port: 5353, State: inspect.StateBound, IPv4: true, IPv6: true},
			},
		},
		{
			name: "different addresses, protocols and states stay apart in first-seen order",
			sockets: []inspect.Socket{
				tcpSocket(inspect.IPv6, "::1"),
				tcpSocket(inspect.IPv4, "127.0.0.1"),
				udpSocket(inspect.IPv4, "127.0.0.1", 3000),
				{Protocol: inspect.TCP, Family: inspect.IPv4, Address: "127.0.0.1", Port: 3000, State: inspect.StateBound},
			},
			want: []Binding{
				{Protocol: inspect.TCP, Address: "::1", Port: 3000, State: inspect.StateListen, IPv6: true},
				{Protocol: inspect.TCP, Address: "127.0.0.1", Port: 3000, State: inspect.StateListen, IPv4: true},
				{Protocol: inspect.UDP, Address: "127.0.0.1", Port: 3000, State: inspect.StateBound, IPv4: true},
				{Protocol: inspect.TCP, Address: "127.0.0.1", Port: 3000, State: inspect.StateBound, IPv4: true},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := CollapseBindings(tt.sockets); !slices.Equal(got, tt.want) {
				t.Errorf("CollapseBindings = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestBinding_Words(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		binding      Binding
		wantString   string
		wantFamily   string
		wantState    string
		wantExposure string
	}{
		{
			name:         "loopback listener",
			binding:      Binding{Protocol: inspect.TCP, Address: "127.0.0.1", Port: 3000, State: inspect.StateListen, IPv4: true},
			wantString:   "127.0.0.1:3000",
			wantFamily:   "IPv4",
			wantState:    "listening",
			wantExposure: "loopback only",
		},
		{
			name:         "dual-stack udp wildcard",
			binding:      Binding{Protocol: inspect.UDP, Address: "*", Port: 5353, State: inspect.StateBound, IPv4: true, IPv6: true},
			wantString:   "*:5353 (v4+v6)",
			wantFamily:   "IPv4+IPv6",
			wantState:    "bound",
			wantExposure: "all interfaces",
		},
		{
			name:         "tcp bound to a link-local address",
			binding:      Binding{Protocol: inspect.TCP, Address: "fe80::1%lo0", Port: 3000, State: inspect.StateBound, IPv6: true},
			wantString:   "[fe80::1%lo0]:3000",
			wantFamily:   "IPv6",
			wantState:    "bound, not listening",
			wantExposure: "interface fe80::1%lo0",
		},
		{
			name:         "wildcard bound to a device",
			binding:      Binding{Protocol: inspect.TCP, Address: "*%eth0", Port: 80, State: inspect.StateListen, IPv4: true},
			wantString:   "*%eth0:80",
			wantFamily:   "IPv4",
			wantState:    "listening",
			wantExposure: "interface eth0",
		},
		{
			name:         "unclassifiable address and unknown family",
			binding:      Binding{Protocol: inspect.TCP, Address: "bogus", Port: 80, State: "CLOSED"},
			wantString:   "bogus:80",
			wantFamily:   "unknown family",
			wantState:    "closed",
			wantExposure: "unknown",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := tt.binding
			got := []string{b.String(), b.familyLabel(), b.stateLabel(), b.exposureWord()}
			want := []string{tt.wantString, tt.wantFamily, tt.wantState, tt.wantExposure}
			if !slices.Equal(got, want) {
				t.Errorf("string, family, state, exposure = %q, want %q", got, want)
			}
		})
	}
}

func TestPIDText(t *testing.T) {
	t.Parallel()

	for pid, want := range map[int]string{0: "-", -1: "-", 48213: "48213"} {
		if got := PIDText(pid); got != want {
			t.Errorf("PIDText(%d) = %q, want %q", pid, got, want)
		}
	}
}

func TestShortCommand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		process string
		command string
		want    string
	}{
		{name: "relative argv0", process: "node", command: "node server.js", want: "node server.js"},
		{name: "absolute argv0", process: "node", command: "/usr/local/bin/node /app/server.js", want: "node /app/server.js"},
		{name: "no arguments", process: "mDNSResponder", command: "/usr/sbin/mDNSResponder", want: "mDNSResponder"},
		{
			name:    "app bundle path with spaces",
			process: "Google Chrome Helper",
			command: chromeCommand,
			want: "Google Chrome Helper --type=utility --utility-sub-type=network.mojom.NetworkService --lang=en-US " +
				"--service-sandbox-type=network --shared-files --field-trial-handle=1718379636,r,358309612294444706",
		},
		{
			name:    "app bundle path with spaces and no arguments",
			process: "Codex (Service)",
			command: "/Applications/ChatGPT.app/Contents/MacOS/Codex (Service)",
			want:    "Codex (Service)",
		},
		{
			name:    "name inside an argument is not argv0",
			process: "node",
			command: "node /usr/bin/node script.js",
			want:    "node /usr/bin/node script.js",
		},
		{
			name:    "process name differs from argv0",
			process: "Python",
			command: "/opt/homebrew/bin/python3 -m http.server",
			want:    "python3 -m http.server",
		},
		{
			name:    "quoted windows path",
			process: "node.exe",
			command: `"C:\Program Files\nodejs\node.exe" server.js`,
			want:    "node.exe server.js",
		},
		{
			name:    "unquoted windows path with spaces",
			process: "svc.exe",
			command: `C:\Program Files\Svc\svc.exe -k run`,
			want:    "svc.exe -k run",
		},
		{name: "empty", process: "node", command: "", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := shortCommand(tt.process, tt.command); got != tt.want {
				t.Errorf("shortCommand(%q, %q) = %q, want %q", tt.process, tt.command, got, tt.want)
			}
		})
	}
}

func TestTruncate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		s     string
		width int
		want  string
	}{
		{name: "fits", s: "node", width: 4, want: "node"},
		{name: "cut", s: "node server.js", width: 8, want: "node se…"},
		{name: "space before the cut is dropped", s: "node server.js", width: 6, want: "node…"},
		{name: "one cell", s: "node", width: 1, want: "…"},
		{name: "no room", s: "node", width: 0, want: ""},
		{name: "wide runes", s: "日本語のポート", width: 5, want: "日本…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := Truncate(tt.s, tt.width); got != tt.want {
				t.Errorf("Truncate(%q, %d) = %q, want %q", tt.s, tt.width, got, tt.want)
			}
		})
	}
}
