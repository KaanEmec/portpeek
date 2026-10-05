package ss

import (
	"cmp"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"github.com/kaanemec/portpeek/internal/inspect"
)

// unknownPID is the PID of the Owner that holds sockets ss reported without
// any process, which happens for other users' sockets without privileges.
const unknownPID = 0

// reasonHidden is recorded for every field of the unknown owner.
const reasonHidden = "not readable without elevated privileges"

// row is one line of `ss -H -n -p` output.
type row struct {
	protocol inspect.Protocol // Netid column, or the query's protocol when ss omits it
	state    string           // "LISTEN", "UNCONN", "ESTAB", ...
	local    string           // "127.0.0.1:3000", "[::]:3000", "*%eth0:3000", ...
	peer     string           // "0.0.0.0:*", "[::]:*", "127.0.0.1:60166", ...
	users    []processRef     // empty when ss could not attribute the socket
}

// processRef is one ("name",pid=N,fd=M) entry of the users:(...) column.
type processRef struct {
	name string
	pid  int
}

// parseRows parses ss output into rows. ss prints a Netid column only when
// more than one socket table is queried, so for a single-protocol query
// rows start with the state and fallback supplies the protocol.
//
//	tcp LISTEN 0 4096 127.0.0.1:3000 0.0.0.0:* users:(("node",pid=42,fd=21))
//	LISTEN 0 4096 127.0.0.1:3000 0.0.0.0:* users:(("node",pid=42,fd=21))
func parseRows(out []byte, fallback inspect.Protocol) ([]row, error) {
	rows := []row{}
	for line := range strings.Lines(string(out)) {
		if strings.TrimSpace(line) == "" {
			continue
		}
		r, err := parseRow(line, fallback)
		if err != nil {
			return nil, fmt.Errorf("line %q: %w", strings.TrimSpace(line), err)
		}
		rows = append(rows, r)
	}
	return rows, nil
}

func parseRow(line string, fallback inspect.Protocol) (row, error) {
	var r row
	first, rest := nextField(line)
	switch first {
	case "tcp", "udp":
		r.protocol = inspect.Protocol(first)
		r.state, rest = nextField(rest)
	default:
		if fallback == "" {
			return row{}, fmt.Errorf("missing Netid column")
		}
		r.protocol = fallback
		r.state = first
	}

	// Recv-Q and Send-Q are not needed.
	_, rest = nextField(rest)
	_, rest = nextField(rest)
	r.local, rest = nextField(rest)
	r.peer, rest = nextField(rest)
	if r.peer == "" {
		return row{}, fmt.Errorf("too few columns")
	}

	// The process column is the remainder of the line: process names may
	// contain spaces.
	users, err := parseUsers(strings.TrimSpace(rest))
	if err != nil {
		return row{}, err
	}
	r.users = users
	return r, nil
}

// nextField returns the first whitespace-separated field of s and the text
// after it.
func nextField(s string) (field, rest string) {
	s = strings.TrimLeft(s, " \t\r\n")
	end := strings.IndexAny(s, " \t\r\n")
	if end < 0 {
		return s, ""
	}
	return s[:end], s[end:]
}

// parseUsers parses the process column, for example
//
//	users:(("exe",pid=1013,fd=3),("sockets",pid=1005,fd=14))
//
// An empty column yields no entries.
func parseUsers(s string) ([]processRef, error) {
	if s == "" {
		return []processRef{}, nil
	}
	inner, ok := strings.CutPrefix(s, "users:(")
	if !ok || !strings.HasSuffix(inner, ")") {
		return nil, fmt.Errorf("unexpected process column %q", s)
	}
	inner = inner[:len(inner)-1]

	refs := []processRef{}
	for inner != "" {
		entry, ok := strings.CutPrefix(inner, `("`)
		if !ok {
			return nil, fmt.Errorf("unexpected process entry %q", inner)
		}
		name, after, ok := strings.Cut(entry, `",pid=`)
		if !ok {
			return nil, fmt.Errorf("process entry without pid %q", inner)
		}
		end := strings.IndexAny(after, ",)")
		if end < 0 {
			return nil, fmt.Errorf("unterminated process entry %q", inner)
		}
		pid, err := strconv.Atoi(after[:end])
		if err != nil || pid <= 0 {
			return nil, fmt.Errorf("invalid pid in process entry %q", inner)
		}
		refs = append(refs, processRef{name: name, pid: pid})

		// Skip any further attributes (fd=...) up to the closing parenthesis
		// and the separating comma.
		closing := strings.IndexByte(after, ')')
		if closing < 0 {
			return nil, fmt.Errorf("unterminated process entry %q", inner)
		}
		inner = strings.TrimPrefix(after[closing+1:], ",")
	}
	return refs, nil
}

// buildOwners applies the ownership rule to parsed rows and groups the
// surviving sockets by PID. A process owns the queried port only through a
// socket bound to that port locally with no peer. Sockets ss could not
// attribute to a process are grouped under one Owner with PID unknownPID,
// because their owner cannot be told apart. Owners are sorted by PID with
// the unknown owner last; sockets by protocol, family, then address.
func buildOwners(rows []row, q inspect.Query) []inspect.Owner {
	byPID := map[int]*inspect.Owner{}
	add := func(pid int, name string, s inspect.Socket) {
		owner, seen := byPID[pid]
		if !seen {
			owner = &inspect.Owner{Process: newProcess(pid, name), Sockets: []inspect.Socket{}}
			byPID[pid] = owner
		}
		owner.Sockets = append(owner.Sockets, s)
	}

	for _, r := range rows {
		s, ok := ownedSocket(r, q)
		if !ok {
			continue
		}
		if len(r.users) == 0 {
			add(unknownPID, "", s)
			continue
		}
		for _, u := range r.users {
			add(u.pid, u.name, s)
		}
	}

	owners := make([]inspect.Owner, 0, len(byPID))
	for _, o := range byPID {
		slices.SortFunc(o.Sockets, compareSockets)
		// A process holding the socket through several descriptors is listed
		// once per descriptor in the users column.
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
// owner gets every field marked unavailable.
func newProcess(pid int, name string) inspect.Process {
	p := inspect.Process{PID: pid, Name: name, Unavailable: map[inspect.Field]string{}}
	if pid == unknownPID {
		for _, f := range []inspect.Field{
			inspect.FieldName,
			inspect.FieldUser,
			inspect.FieldCommand,
			inspect.FieldWorkingDir,
		} {
			p.MarkUnavailable(f, reasonHidden)
		}
	}
	return p
}

// ownedSocket converts a row into a Socket if the socket owns the queried
// port according to the ownership rule.
func ownedSocket(r row, q inspect.Query) (inspect.Socket, bool) {
	if r.protocol != inspect.TCP && r.protocol != inspect.UDP {
		return inspect.Socket{}, false
	}
	if q.Protocol != "" && q.Protocol != r.protocol {
		return inspect.Socket{}, false
	}
	// A socket with a concrete peer (an established connection or a
	// connected UDP socket) never owns the port, whatever its state.
	if !isWildcardPeer(r.peer) {
		return inspect.Socket{}, false
	}
	addr, family, port, ok := parseLocal(r.local)
	if !ok || port != q.Port {
		return inspect.Socket{}, false
	}

	// ss reports UDP sockets and TCP sockets bound without listen as UNCONN;
	// any peerless state other than LISTEN still holds the port.
	state := inspect.StateBound
	if r.protocol == inspect.TCP && r.state == "LISTEN" {
		state = inspect.StateListen
	}
	return inspect.Socket{
		Protocol: r.protocol,
		Family:   family,
		Address:  addr,
		Port:     port,
		State:    state,
	}, true
}

// isWildcardPeer reports whether a peer column means "no peer": "0.0.0.0:*",
// "[::]:*" or "*:*".
func isWildcardPeer(peer string) bool {
	host, ok := strings.CutSuffix(peer, ":*")
	if !ok {
		return false
	}
	host, _, _ = strings.Cut(host, "%")
	return host == "*" || host == "0.0.0.0" || host == "[::]"
}

// parseLocal parses an ss local address column into the model's address,
// family and port. ss prints:
//
//	127.0.0.1:3000          IPv4
//	0.0.0.0:3000            IPv4 wildcard
//	[::1]:3000              IPv6
//	[::]:3000               IPv6 wildcard, IPv6 only
//	*:3000                  IPv6 wildcard accepting IPv4 too (dual stack)
//	[fe80::1]%eth0:3000     link-local, or any socket bound to a device
//	0.0.0.0%eth0:3000       wildcard bound to a device (SO_BINDTODEVICE)
//	*%eth0:3000
//
// Wildcards become "*", matching the macOS adapter, and an interface is kept
// as a zone: "fe80::1%eth0", "*%eth0". ok is false for a malformed column.
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

	var ipStr, zone string
	if strings.HasPrefix(host, "[") {
		end := strings.IndexByte(host, ']')
		if end < 0 {
			return "", "", 0, false
		}
		ipStr = host[1:end]
		// The zone may follow the bracket or, in older iproute2 releases,
		// sit inside it: "[fe80::1]%eth0" or "[fe80::1%eth0]".
		if after := host[end+1:]; after != "" {
			z, found := strings.CutPrefix(after, "%")
			if !found {
				return "", "", 0, false
			}
			zone = z
		}
		if ip, z, found := strings.Cut(ipStr, "%"); found {
			ipStr, zone = ip, z
		}
	} else {
		ipStr, zone, _ = strings.Cut(host, "%")
	}

	if ipStr == "*" {
		return withZone("*", zone), inspect.IPv6, port, true
	}
	ip, err := netip.ParseAddr(ipStr)
	if err != nil {
		return "", "", 0, false
	}
	family = inspect.IPv4
	if ip.Is6() {
		family = inspect.IPv6
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
		cmp.Compare(a.Protocol, b.Protocol),
		cmp.Compare(a.Family, b.Family),
		cmp.Compare(a.Address, b.Address),
		cmp.Compare(a.Port, b.Port),
		cmp.Compare(a.State, b.State),
	)
}
