package netstat

import (
	"cmp"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"github.com/kaanemec/portpeek/internal/inspect"
)

// unknownPID is the PID of the Owner that holds sockets netstat reported
// with PID 0, which it does when it cannot attribute a socket to a process.
const unknownPID = 0

// systemPID is the PID of the Windows System process, which owns kernel-mode
// listeners such as http.sys (port 80) and SMB (port 445). Win32_Process
// reports neither a command line nor an executable path for it.
const systemPID = 4

// row is one TCP or UDP line of `netstat -a -n -o` output.
type row struct {
	protocol inspect.Protocol
	local    string // "127.0.0.1:3000", "[::]:3000", "[fe80::1%12]:3000"
	peer     string // "0.0.0.0:0", "[::]:0", "*:*", "127.0.0.1:52000"
	state    string // "LISTENING", "BOUND", "ESTABLISHED", ...; empty for UDP
	pid      int
}

// parseRows parses netstat output into rows. Lines whose first column is not
// TCP or UDP are skipped: the "Active Connections" title, the column header
// and blank lines, all of which Windows localizes. Rows look like
//
//	TCP    127.0.0.1:3000         0.0.0.0:0              LISTENING       1234
//	UDP    0.0.0.0:3000           *:*                                    1234
//
// TCP rows have a state column; UDP rows do not. The PID is always last.
func parseRows(out []byte) ([]row, error) {
	rows := []row{}
	for line := range strings.Lines(string(out)) {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		var (
			r   row
			err error
		)
		switch fields[0] {
		case "TCP":
			r, err = parseTCP(fields)
		case "UDP":
			r, err = parseUDP(fields)
		default:
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("line %q: %w", strings.TrimSpace(line), err)
		}
		rows = append(rows, r)
	}
	return rows, nil
}

// parseTCP parses the fields of a TCP row: protocol, local address, foreign
// address, state and PID. The state is joined from every field between the
// foreign address and the PID, in case a localized state contains spaces.
func parseTCP(fields []string) (row, error) {
	if len(fields) < 5 {
		return row{}, fmt.Errorf("too few columns")
	}
	pid, err := parsePID(fields[len(fields)-1])
	if err != nil {
		return row{}, err
	}
	return row{
		protocol: inspect.TCP,
		local:    fields[1],
		peer:     fields[2],
		state:    strings.Join(fields[3:len(fields)-1], " "),
		pid:      pid,
	}, nil
}

// parseUDP parses the fields of a UDP row: protocol, local address, foreign
// address and PID.
func parseUDP(fields []string) (row, error) {
	if len(fields) != 4 {
		return row{}, fmt.Errorf("want 4 columns, got %d", len(fields))
	}
	pid, err := parsePID(fields[3])
	if err != nil {
		return row{}, err
	}
	return row{protocol: inspect.UDP, local: fields[1], peer: fields[2], pid: pid}, nil
}

func parsePID(s string) (int, error) {
	pid, err := strconv.Atoi(s)
	if err != nil || pid < 0 {
		return 0, fmt.Errorf("invalid pid %q", s)
	}
	return pid, nil
}

// buildOwners applies the ownership rule to parsed rows and groups the
// surviving sockets by PID. A process owns the queried port only through a
// socket bound to that port locally with no peer; q.Port 0 matches every
// port, which is how List uses it. Sockets netstat reported with PID 0 are
// grouped under one Owner with PID unknownPID, because their owner cannot
// be told apart. Owners are sorted by PID with the unknown owner last;
// sockets by port, protocol, family, then address.
func buildOwners(rows []row, q inspect.Query) []inspect.Owner {
	byPID := map[int]*inspect.Owner{}
	for _, r := range rows {
		s, ok := ownedSocket(r, q)
		if !ok {
			continue
		}
		owner, seen := byPID[r.pid]
		if !seen {
			owner = &inspect.Owner{Process: newProcess(r.pid), Sockets: []inspect.Socket{}}
			byPID[r.pid] = owner
		}
		owner.Sockets = append(owner.Sockets, s)
	}

	owners := make([]inspect.Owner, 0, len(byPID))
	for _, o := range byPID {
		slices.SortFunc(o.Sockets, compareSockets)
		// The per-protocol tables should not overlap, but a duplicate row
		// must not show up as a second socket.
		o.Sockets = slices.Compact(o.Sockets)
		owners = append(owners, *o)
	}
	slices.SortFunc(owners, func(a, b inspect.Owner) int {
		return comparePIDs(a.Process.PID, b.Process.PID)
	})
	return owners
}

// comparePIDs orders PIDs ascending with unknownPID last.
func comparePIDs(a, b int) int {
	switch {
	case a == b:
		return 0
	case a == unknownPID:
		return 1
	case b == unknownPID:
		return -1
	default:
		return cmp.Compare(a, b)
	}
}

// newProcess builds the Process for a PID before enrichment. The unknown
// owner gets every field marked unavailable. Every other owner has User and
// WorkingDir marked unavailable, because the Windows sources this adapter
// uses do not report them; the System process is named here and has no
// command line.
func newProcess(pid int) inspect.Process {
	p := inspect.Process{PID: pid, Unavailable: map[inspect.Field]string{}}
	if pid == unknownPID {
		for _, f := range []inspect.Field{
			inspect.FieldName,
			inspect.FieldUser,
			inspect.FieldCommand,
			inspect.FieldWorkingDir,
		} {
			p.MarkUnavailable(f, reasonNotReported)
		}
		return p
	}
	p.MarkUnavailable(inspect.FieldUser, reasonNotOnWindows)
	p.MarkUnavailable(inspect.FieldWorkingDir, reasonNotOnWindows)
	if pid == systemPID {
		p.Name = "System"
		p.MarkUnavailable(inspect.FieldCommand, reasonSystemProcess)
	}
	return p
}

// needsEnrichment reports whether a process's details come from PowerShell.
// The unknown owner has no process to query, and the System process is
// fully described by newProcess.
func needsEnrichment(pid int) bool {
	return pid != unknownPID && pid != systemPID
}

// ownedSocket converts a row into a Socket if the socket owns the queried
// port according to the ownership rule. A query for port 0 accepts any
// port.
func ownedSocket(r row, q inspect.Query) (inspect.Socket, bool) {
	if q.Protocol != "" && q.Protocol != r.protocol {
		return inspect.Socket{}, false
	}
	// A socket with a concrete peer (an established connection) never owns
	// the port, whatever its state.
	if !isWildcardPeer(r.peer) {
		return inspect.Socket{}, false
	}
	addr, family, port, ok := parseLocal(r.local)
	if !ok || !matchesPort(port, q.Port) {
		return inspect.Socket{}, false
	}

	var state string
	switch {
	case r.protocol == inspect.UDP:
		state = inspect.StateBound
	case r.state == "LISTENING":
		state = inspect.StateListen
	case r.state == "BOUND":
		state = inspect.StateBound
	default:
		// TIME_WAIT, CLOSED and any state this adapter does not know never
		// claim ownership.
		return inspect.Socket{}, false
	}
	return inspect.Socket{
		Protocol: r.protocol,
		Family:   family,
		Address:  addr,
		Port:     port,
		State:    state,
	}, true
}

// matchesPort reports whether a socket's local port satisfies a wanted port,
// where wanted 0 means any port. Local port 0 is never bound, so it never
// matches.
func matchesPort(port, wanted int) bool {
	return port != 0 && (wanted == 0 || port == wanted)
}

// isWildcardPeer reports whether a foreign address column means "no peer":
// "0.0.0.0:0" or "[::]:0" for TCP, "*:*" for UDP.
func isWildcardPeer(peer string) bool {
	i := strings.LastIndexByte(peer, ':')
	if i < 0 {
		return false
	}
	host, port := peer[:i], peer[i+1:]
	if port != "0" && port != "*" {
		return false
	}
	if inner, ok := strings.CutPrefix(host, "["); ok {
		host, ok = strings.CutSuffix(inner, "]")
		if !ok {
			return false
		}
	}
	host, _, _ = strings.Cut(host, "%")
	return host == "*" || host == "0.0.0.0" || host == "::"
}

// parseLocal parses a netstat local address column into the model's
// address, family and port. netstat prints:
//
//	127.0.0.1:3000          IPv4
//	0.0.0.0:3000            IPv4 wildcard
//	[::1]:3000              IPv6
//	[::]:3000               IPv6 wildcard
//	[fe80::1%12]:3000       link-local, zone is the interface index
//
// Brackets mark IPv6. Wildcards become "*", matching the other adapters,
// and a zone is kept: "fe80::1%12". ok is false for a malformed column.
func parseLocal(s string) (addr string, family inspect.Family, port int, ok bool) {
	i := strings.LastIndexByte(s, ':')
	if i < 0 {
		return "", "", 0, false
	}
	host, portStr := s[:i], s[i+1:]
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 0 || port > 65535 {
		return "", "", 0, false
	}

	family = inspect.IPv4
	var zone string
	if rest, bracketed := strings.CutPrefix(host, "["); bracketed {
		inner, closed := strings.CutSuffix(rest, "]")
		if !closed {
			return "", "", 0, false
		}
		family = inspect.IPv6
		host, zone, _ = strings.Cut(inner, "%")
	}

	ip, err := netip.ParseAddr(host)
	if err != nil || ip.Is6() != (family == inspect.IPv6) {
		return "", "", 0, false
	}
	addr = ip.String()
	if ip.IsUnspecified() {
		addr = "*"
	}
	return withZone(addr, zone), family, port, true
}

func withZone(addr, zone string) string {
	if zone == "" {
		return addr
	}
	return addr + "%" + zone
}

func compareSockets(a, b inspect.Socket) int {
	return cmp.Or(
		cmp.Compare(a.Port, b.Port),
		cmp.Compare(a.Protocol, b.Protocol),
		cmp.Compare(a.Family, b.Family),
		cmp.Compare(a.Address, b.Address),
		cmp.Compare(a.State, b.State),
	)
}
