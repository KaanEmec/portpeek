package tui

import (
	"cmp"
	"net"
	"slices"
	"strconv"
	"strings"

	"github.com/kaanemec/portpeek/internal/inspect"
)

// row is one socket in the table.
type row struct {
	port    int
	proto   inspect.Protocol
	address string
	// process is the owner's name, or "unknown" when it has none.
	process  string
	pid      int
	exposure inspect.Exposure
}

// rowKey identifies a socket across refreshes, so the selection follows it
// when rows are added, removed or reordered.
type rowKey struct {
	port    int
	proto   inspect.Protocol
	address string
}

func (r row) key() rowKey {
	return rowKey{port: r.port, proto: r.proto, address: r.address}
}

// binding is the bound address and port, with IPv6 bracketed.
func (r row) binding() string {
	return net.JoinHostPort(r.address, strconv.Itoa(r.port))
}

// pidText is the PID, or "-" for an owner the OS did not attribute.
func (r row) pidText() string {
	if r.pid <= 0 {
		return "-"
	}
	return strconv.Itoa(r.pid)
}

// exposureLabel is a short form of the CLI's exposure wording that fits a
// column.
func exposureLabel(e inspect.Exposure) string {
	switch e {
	case inspect.ExposureLoopback:
		return "loopback"
	case inspect.ExposureAllInterfaces:
		return "all interfaces"
	case inspect.ExposureInterface:
		return "interface"
	default:
		return "unknown"
	}
}

// buildRows flattens a snapshot into one row per socket.
func buildRows(snap inspect.Snapshot) []row {
	rows := []row{}
	for _, o := range snap.Owners {
		p := o.Process
		name := p.Name
		if name == "" || p.PID <= 0 {
			name = "unknown"
		}
		for _, s := range o.Sockets {
			rows = append(rows, row{
				port:     s.Port,
				proto:    s.Protocol,
				address:  s.Address,
				process:  name,
				pid:      p.PID,
				exposure: s.Exposure(),
			})
		}
	}
	return rows
}

// sortMode is the table order.
type sortMode int

const (
	sortByPort sortMode = iota
	sortByProcess
)

func (s sortMode) String() string {
	if s == sortByProcess {
		return "process"
	}
	return "port"
}

// next returns the other sort mode.
func (s sortMode) next() sortMode {
	if s == sortByPort {
		return sortByProcess
	}
	return sortByPort
}

// sortRows orders rows in place: by port then protocol, or by process name
// (case-insensitive) then port. Remaining ties fall back to address and PID
// so the order is stable across refreshes.
func sortRows(rows []row, mode sortMode) {
	byPort := func(a, b row) int {
		return cmp.Or(
			cmp.Compare(a.port, b.port),
			cmp.Compare(a.proto, b.proto),
			cmp.Compare(a.address, b.address),
			cmp.Compare(a.pid, b.pid),
		)
	}
	if mode == sortByPort {
		slices.SortFunc(rows, byPort)
		return
	}
	slices.SortFunc(rows, func(a, b row) int {
		return cmp.Or(
			cmp.Compare(strings.ToLower(a.process), strings.ToLower(b.process)),
			byPort(a, b),
		)
	})
}

// filterRows returns the rows whose port starts with query or whose process
// name contains it, ignoring case. An empty query keeps every row.
func filterRows(rows []row, query string) []row {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return rows
	}
	kept := []row{}
	for _, r := range rows {
		portMatch := strings.HasPrefix(strconv.Itoa(r.port), query)
		if portMatch || strings.Contains(strings.ToLower(r.process), query) {
			kept = append(kept, r)
		}
	}
	return kept
}
