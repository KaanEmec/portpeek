package cli

import (
	"net"
	"slices"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/kaanemec/portpeek/internal/inspect"
)

// Binding is one address an owner holds on its port. Sockets that differ only
// by address family, such as the IPv4 and IPv6 halves of a dual-stack
// wildcard listener, collapse into one Binding. The CLI and the terminal
// interface both describe ports in terms of bindings.
type Binding struct {
	Protocol inspect.Protocol
	Address  string
	Port     int
	State    string
	// IPv4 and IPv6 report which address families hold the binding.
	IPv4, IPv6 bool
}

// CollapseBindings groups sockets into bindings in first-seen order: sockets
// with the same protocol, address, port and state become one binding that
// records each family.
func CollapseBindings(sockets []inspect.Socket) []Binding {
	bindings := make([]Binding, 0, len(sockets))
	for _, s := range sockets {
		i := slices.IndexFunc(bindings, func(b Binding) bool {
			return b.Protocol == s.Protocol && b.Address == s.Address && b.Port == s.Port && b.State == s.State
		})
		if i < 0 {
			bindings = append(bindings, Binding{Protocol: s.Protocol, Address: s.Address, Port: s.Port, State: s.State})
			i = len(bindings) - 1
		}
		switch s.Family {
		case inspect.IPv4:
			bindings[i].IPv4 = true
		case inspect.IPv6:
			bindings[i].IPv6 = true
		}
	}
	return bindings
}

// String is the address and port, with IPv6 bracketed and a dual-stack
// binding marked: "127.0.0.1:3000", "[::1]:3000", "*:5353 (v4+v6)".
func (b Binding) String() string {
	if b.IPv4 && b.IPv6 {
		return b.hostPort() + " (v4+v6)"
	}
	return b.hostPort()
}

// hostPort is the address and port with IPv6 bracketed, without the
// dual-stack marker.
func (b Binding) hostPort() string {
	return net.JoinHostPort(b.Address, strconv.Itoa(b.Port))
}

// Exposure classifies the binding's address.
func (b Binding) Exposure() inspect.Exposure {
	return inspect.Socket{Address: b.Address}.Exposure()
}

// familyLabel names the binding's address families.
func (b Binding) familyLabel() string {
	switch {
	case b.IPv4 && b.IPv6:
		return "IPv4+IPv6"
	case b.IPv4:
		return "IPv4"
	case b.IPv6:
		return "IPv6"
	default:
		return "unknown family"
	}
}

// stateLabel describes how the binding holds its port. A bound TCP socket is
// called out because it holds the port without accepting connections.
func (b Binding) stateLabel() string {
	switch {
	case b.State == inspect.StateListen:
		return "listening"
	case b.State == inspect.StateBound && b.Protocol == inspect.TCP:
		return "bound, not listening"
	case b.State == inspect.StateBound:
		return "bound"
	default:
		return strings.ToLower(b.State)
	}
}

// exposureWord is the short exposure of the binding, naming the address when
// it is bound to one interface: "loopback only", "interface 192.168.1.5".
func (b Binding) exposureWord() string {
	e := b.Exposure()
	if e != inspect.ExposureInterface {
		return ExposureLabel(e)
	}
	// A wildcard bound to one device reads better as the device name.
	if zone, ok := strings.CutPrefix(b.Address, "*%"); ok {
		return ExposureLabel(e) + " " + zone
	}
	return ExposureLabel(e) + " " + b.Address
}

// exposureNote explains an exposure in a short parenthetical for --detail,
// or returns "" when there is nothing to add.
func exposureNote(e inspect.Exposure) string {
	switch e {
	case inspect.ExposureLoopback:
		return "(this machine only)"
	case inspect.ExposureAllInterfaces:
		return "(every interface, firewall not checked)"
	case inspect.ExposureInterface:
		return "(that address only, firewall not checked)"
	default:
		return ""
	}
}

// ExposureLabel is the short wording of an exposure: "loopback only", "all
// interfaces", "interface" or "unknown".
func ExposureLabel(e inspect.Exposure) string {
	switch e {
	case inspect.ExposureLoopback:
		return "loopback only"
	case inspect.ExposureAllInterfaces:
		return "all interfaces"
	case inspect.ExposureInterface:
		return "interface"
	default:
		return "unknown"
	}
}

// PIDText is the PID, or "-" for an owner the OS did not attribute.
func PIDText(pid int) string {
	if pid <= 0 {
		return "-"
	}
	return strconv.Itoa(pid)
}

// shortCommand shortens a command line for one line of output: argv[0] is
// reduced to its base name and the arguments are kept as they are.
func shortCommand(name, command string) string {
	argv0, args := splitArgv0(name, strings.TrimSpace(command))
	if i := strings.LastIndexAny(argv0, `/\`); i >= 0 && i < len(argv0)-1 {
		argv0 = argv0[i+1:]
	}
	return argv0 + args
}

// splitArgv0 splits a command line into argv[0] and the rest, which keeps its
// leading space. A quoted argv[0] ends at the closing quote. An absolute
// argv[0] that contains spaces, such as a macOS app bundle path, is found by
// the process name, which is its base name; otherwise argv[0] ends at the
// first space.
func splitArgv0(name, command string) (argv0, args string) {
	if quoted, ok := strings.CutPrefix(command, `"`); ok {
		if end := strings.IndexByte(quoted, '"'); end >= 0 {
			return quoted[:end], quoted[end+1:]
		}
	}

	first, rest, _ := strings.Cut(command, " ")
	if rest != "" {
		rest = " " + rest
	}
	absolute := strings.HasPrefix(command, "/") || strings.HasPrefix(command[min(1, len(command)):], `:\`)
	if name == "" || !absolute || strings.HasSuffix(first, name) {
		return first, rest
	}
	for _, sep := range []string{"/", `\`} {
		if i := strings.Index(command, sep+name+" "); i >= 0 {
			end := i + len(sep) + len(name)
			return command[:end], command[end:]
		}
		if strings.HasSuffix(command, sep+name) {
			return command, ""
		}
	}
	return first, rest
}

// Truncate cuts s to at most w display cells, ending in "…" when cut. A
// space right before the cut is dropped.
func Truncate(s string, w int) string {
	if lipgloss.Width(s) <= w {
		return s
	}
	if w <= 0 {
		return ""
	}
	var b strings.Builder
	used := 0
	for _, r := range s {
		rw := lipgloss.Width(string(r))
		if used+rw > w-1 {
			break
		}
		b.WriteRune(r)
		used += rw
	}
	return strings.TrimRight(b.String(), " ") + "…"
}
