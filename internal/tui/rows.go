package tui

import (
	"cmp"
	"slices"
	"strconv"
	"strings"

	"github.com/kaanemec/portpeek/internal/cli"
	"github.com/kaanemec/portpeek/internal/inspect"
)

// row is one binding in the table: the sockets of one owner that differ
// only by address family share a row, as in the CLI's output.
type row struct {
	bind cli.Binding
	// process is the owner's name, or "unknown" when it has none.
	process string
	pid     int
}

// rowKey identifies a binding across refreshes, so the selection follows it
// when rows are added, removed or reordered.
type rowKey struct {
	port    int
	proto   inspect.Protocol
	address string
}

func (r row) key() rowKey {
	return rowKey{port: r.bind.Port, proto: r.bind.Protocol, address: r.bind.Address}
}

// buildRows flattens a snapshot into one row per binding, collapsed with the
// CLI's rules: both families of a dual-stack wildcard listener share one row
// marked "(v4+v6)". The details view lists every socket.
func buildRows(snap inspect.Snapshot) []row {
	rows := []row{}
	seen := map[row]bool{}
	for _, o := range snap.Owners {
		p := o.Process
		name := p.Name
		if name == "" || p.PID <= 0 {
			name = "unknown"
		}
		for _, b := range cli.CollapseBindings(o.Sockets) {
			r := row{bind: b, process: name, pid: p.PID}
			if !seen[r] {
				seen[r] = true
				rows = append(rows, r)
			}
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
			cmp.Compare(a.bind.Port, b.bind.Port),
			cmp.Compare(a.bind.Protocol, b.bind.Protocol),
			cmp.Compare(a.bind.Address, b.bind.Address),
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

// filterRows returns a new slice of the rows whose port starts with query or
// whose process name contains it, ignoring case. An empty query keeps every
// row.
func filterRows(rows []row, query string) []row {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return slices.Clone(rows)
	}
	kept := []row{}
	for _, r := range rows {
		portMatch := strings.HasPrefix(strconv.Itoa(r.bind.Port), query)
		if portMatch || strings.Contains(strings.ToLower(r.process), query) {
			kept = append(kept, r)
		}
	}
	return kept
}
