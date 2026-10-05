package lsof

import (
	"bytes"
	"cmp"
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"github.com/kaanemec/portpeek/internal/inspect"
)

// processRecord is one lsof process set: the "p" field and the process-level
// fields that follow it, plus every file set reported for the process.
type processRecord struct {
	pid   int
	name  string // "c" field
	user  string // "L" field
	files []fileRecord
}

// fileRecord is one lsof file set, started by an "f" field.
type fileRecord struct {
	fd       string // "f" field: "3", "cwd", ...
	family   string // "t" field: "IPv4", "IPv6", "DIR", ...
	protocol string // "P" field: "TCP", "UDP"
	name     string // "n" field: "127.0.0.1:3000", "a:1->b:2", "/path"
	state    string // "T" field "ST=<state>", TCP only
}

// interfaceName resolves a network interface index to its name. It is a
// variable so tests can replace it.
var interfaceName = func(index int) (string, error) {
	ifi, err := net.InterfaceByIndex(index)
	if err != nil {
		return "", err
	}
	return ifi.Name, nil
}

// parseRecords parses lsof -F output into process records. Fields may be
// terminated by NUL (-F ...0) or by newline (plain -F); both are accepted.
// The first byte of each field identifies it. Unknown fields are ignored.
func parseRecords(out []byte) ([]processRecord, error) {
	fields := bytes.FieldsFunc(out, func(r rune) bool { return r == 0 || r == '\n' })
	records := []processRecord{}
	for _, raw := range fields {
		id, value := raw[0], string(raw[1:])
		if id == 'p' {
			pid, err := strconv.Atoi(value)
			if err != nil || pid <= 0 {
				return nil, fmt.Errorf("invalid pid field %q", raw)
			}
			records = append(records, processRecord{pid: pid})
			continue
		}
		if len(records) == 0 {
			return nil, fmt.Errorf("field %q before any process field", raw)
		}
		proc := &records[len(records)-1]
		if id == 'f' {
			proc.files = append(proc.files, fileRecord{fd: value})
			continue
		}
		if len(proc.files) == 0 {
			setProcessField(proc, id, value)
			continue
		}
		setFileField(&proc.files[len(proc.files)-1], id, value)
	}
	return records, nil
}

func setProcessField(p *processRecord, id byte, value string) {
	switch id {
	case 'c':
		p.name = value
	case 'L':
		p.user = value
	}
}

func setFileField(f *fileRecord, id byte, value string) {
	switch id {
	case 't':
		f.family = value
	case 'P':
		f.protocol = value
	case 'n':
		f.name = value
	case 'T':
		// TCP info fields arrive as several "T" fields: ST=, QR=, QS=, ...
		if state, ok := strings.CutPrefix(value, "ST="); ok {
			f.state = state
		}
	}
}

// buildOwners applies the ownership rule to parsed records and groups the
// surviving sockets by PID. A process owns the queried port only through a
// socket bound to that port locally with no peer: a TCP listener, a TCP
// socket bound without listening, or an unconnected UDP socket. q.Port 0
// matches every port, which is how List uses it. Processes left with no
// owned socket are dropped. Owners are sorted by PID; sockets by port,
// protocol, family, then address.
func buildOwners(records []processRecord, q inspect.Query) []inspect.Owner {
	byPID := map[int]*inspect.Owner{}
	for _, rec := range records {
		for _, f := range rec.files {
			s, ok := ownedSocket(f, q)
			if !ok {
				continue
			}
			owner, seen := byPID[rec.pid]
			if !seen {
				owner = &inspect.Owner{Process: newProcess(rec), Sockets: []inspect.Socket{}}
				byPID[rec.pid] = owner
			}
			owner.Sockets = append(owner.Sockets, s)
		}
	}

	owners := make([]inspect.Owner, 0, len(byPID))
	for _, o := range byPID {
		slices.SortFunc(o.Sockets, compareSockets)
		// Duplicated descriptors (dup, inherited fds) report the same socket
		// more than once within a process.
		o.Sockets = slices.Compact(o.Sockets)
		owners = append(owners, *o)
	}
	slices.SortFunc(owners, func(a, b inspect.Owner) int {
		return cmp.Compare(a.Process.PID, b.Process.PID)
	})
	return owners
}

func newProcess(rec processRecord) inspect.Process {
	p := inspect.Process{
		PID:         rec.pid,
		Name:        rec.name,
		User:        rec.user,
		Unavailable: map[inspect.Field]string{},
	}
	if p.Name == "" {
		p.MarkUnavailable(inspect.FieldName, "not reported by lsof")
	}
	if p.User == "" {
		p.MarkUnavailable(inspect.FieldUser, "not reported by lsof")
	}
	return p
}

// ownedSocket converts a file record into a Socket if it is owned by its
// process on the queried port according to the ownership rule. A query for
// port 0 accepts any port.
func ownedSocket(f fileRecord, q inspect.Query) (inspect.Socket, bool) {
	var family inspect.Family
	switch f.family {
	case "IPv4":
		family = inspect.IPv4
	case "IPv6":
		family = inspect.IPv6
	default:
		return inspect.Socket{}, false
	}

	var proto inspect.Protocol
	switch f.protocol {
	case "TCP":
		proto = inspect.TCP
	case "UDP":
		proto = inspect.UDP
	default:
		return inspect.Socket{}, false
	}
	if q.Protocol != "" && q.Protocol != proto {
		return inspect.Socket{}, false
	}

	// A connected socket prints "local->remote" and never owns the port,
	// whatever its state.
	if strings.Contains(f.name, "->") {
		return inspect.Socket{}, false
	}

	host, port, ok := splitAddress(f.name)
	if !ok || !matchesPort(port, q.Port) {
		return inspect.Socket{}, false
	}
	if family == inspect.IPv6 {
		host = unpackScope(host)
	}

	// lsof reports a TCP socket bound without listen as ST=CLOSED; any
	// peerless TCP state other than LISTEN still holds the port.
	state := inspect.StateBound
	if proto == inspect.TCP && f.state == "LISTEN" {
		state = inspect.StateListen
	}
	return inspect.Socket{
		Protocol: proto,
		Family:   family,
		Address:  host,
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

// splitAddress splits an lsof network name such as "127.0.0.1:3000",
// "*:3000" or "[::1]:3000" into its host, without brackets, and port. ok is
// false when the name has no numeric port ("*:*") or is malformed.
func splitAddress(name string) (host string, port int, ok bool) {
	i := strings.LastIndexByte(name, ':')
	if i < 0 {
		return "", 0, false
	}
	host, portStr := name[:i], name[i+1:]
	if strings.HasPrefix(host, "[") {
		if !strings.HasSuffix(host, "]") {
			return "", 0, false
		}
		host = host[1 : len(host)-1]
	}
	if host == "" {
		return "", 0, false
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 0 || port > 65535 {
		return "", 0, false
	}
	return host, port, true
}

// unpackScope rewrites a link-local IPv6 address in which lsof left the
// kernel's embedded interface index: macOS stores the scope of fe80::/10
// addresses in the second 16-bit group, so lsof prints fe80::1%lo0 as
// "fe80:1::1". The index is moved into a zone named after the interface, or
// numbered when the name cannot be looked up. Other addresses are returned
// unchanged.
func unpackScope(host string) string {
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	if !ip.Is6() || !ip.IsLinkLocalUnicast() {
		return host
	}
	b := ip.As16()
	index := int(binary.BigEndian.Uint16(b[2:4]))
	if index == 0 || ip.Zone() != "" {
		return host
	}
	b[2], b[3] = 0, 0

	zone := strconv.Itoa(index)
	if name, err := interfaceName(index); err == nil && name != "" {
		zone = name
	}
	return netip.AddrFrom16(b).WithZone(zone).String()
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

// workingDir returns the "n" field of the cwd file record in the output of
// `lsof -F n -d cwd -a -p PID`, or "" if there is none.
func workingDir(records []processRecord) string {
	for _, rec := range records {
		for _, f := range rec.files {
			if f.fd == "cwd" && f.name != "" {
				return f.name
			}
		}
	}
	return ""
}
