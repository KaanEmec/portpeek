package lsof

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
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
// TCP socket in LISTEN state or a UDP socket with no peer, bound to that
// port locally. Processes left with no owned socket are dropped. Owners are
// sorted by PID; sockets by protocol, family, then address.
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
// process on the queried port according to the ownership rule.
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

	// A connected socket prints "local->remote"; only a listener or an
	// unconnected UDP socket can own the port.
	if strings.Contains(f.name, "->") {
		return inspect.Socket{}, false
	}
	if proto == inspect.TCP && f.state != "LISTEN" {
		return inspect.Socket{}, false
	}

	host, port, err := splitAddress(f.name)
	if err != nil || port != q.Port {
		return inspect.Socket{}, false
	}

	state := f.state
	if proto == inspect.UDP {
		state = ""
	}
	return inspect.Socket{
		Protocol: proto,
		Family:   family,
		Address:  host,
		Port:     port,
		State:    state,
	}, true
}

var errNoPort = errors.New("no numeric port")

// splitAddress splits an lsof network name such as "127.0.0.1:3000",
// "*:3000", "[::1]:3000" or "[fe80::1%en0]:3000" into its host, without
// brackets, and port. "*:*" has no port and returns errNoPort.
func splitAddress(name string) (host string, port int, err error) {
	i := strings.LastIndexByte(name, ':')
	if i < 0 {
		return "", 0, fmt.Errorf("address %q: missing port", name)
	}
	host, portStr := name[:i], name[i+1:]
	if strings.HasPrefix(host, "[") {
		if !strings.HasSuffix(host, "]") {
			return "", 0, fmt.Errorf("address %q: unbalanced brackets", name)
		}
		host = host[1 : len(host)-1]
	}
	if host == "" {
		return "", 0, fmt.Errorf("address %q: empty host", name)
	}
	port, err = strconv.Atoi(portStr)
	if err != nil || port < 0 || port > 65535 {
		return "", 0, fmt.Errorf("address %q: %w", name, errNoPort)
	}
	return host, port, nil
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
