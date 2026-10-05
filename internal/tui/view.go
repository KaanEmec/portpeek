package tui

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/kaanemec/portpeek/internal/cli"
)

// Size used before the terminal reports one.
const (
	defaultWidth  = 80
	defaultHeight = 24
)

// Columns hide as the terminal narrows: Exposure first, then Binding.
const (
	exposureMinWidth = 80
	bindingMinWidth  = 60
)

// Column sizing. The Process column takes the remaining width.
const (
	columnGap       = "  "
	markerWidth     = 2
	maxBindingWidth = 45
	minBindingWidth = len("Binding")
	minProcessWidth = 10
	timeLayout      = "15:04:05"
)

// Colours are downsampled by Bubble Tea to the terminal's profile, so they
// vanish under NO_COLOR and TERM=dumb. Nothing relies on them alone: the
// selected row also carries a ">" marker.
var (
	boldStyle     = lipgloss.NewStyle().Bold(true)
	faintStyle    = lipgloss.NewStyle().Faint(true)
	selectedStyle = lipgloss.NewStyle().Reverse(true)
	errorStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
)

// View renders the current screen in the alternate screen buffer.
func (m model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	return v
}

func (m model) render() string {
	if m.screen == screenDetails {
		return m.renderDetails()
	}
	return m.renderTable()
}

func (m model) viewWidth() int {
	if m.width <= 0 {
		return defaultWidth
	}
	return m.width
}

func (m model) viewHeight() int {
	if m.height <= 0 {
		return defaultHeight
	}
	return m.height
}

// column is one table column at its rendered width.
type column struct {
	title string
	width int
	cell  func(row) string
}

// columns lays out the table for the current width.
func (m model) columns() []column {
	w := m.viewWidth()
	showBinding := w >= bindingMinWidth
	showExposure := w >= exposureMinWidth

	bindingWidth := minBindingWidth
	for _, r := range m.visible {
		bindingWidth = max(bindingWidth, lipgloss.Width(r.binding()))
	}
	bindingWidth = min(bindingWidth, maxBindingWidth)

	port := column{title: "Port", width: 5, cell: func(r row) string { return strconv.Itoa(r.port) }}
	proto := column{title: "Proto", width: 5, cell: func(r row) string { return string(r.proto) }}
	binding := column{title: "Binding", width: bindingWidth, cell: row.binding}
	process := column{title: "Process", cell: func(r row) string { return r.process }}
	pid := column{title: "PID", width: 7, cell: row.pidText}
	exposure := column{title: "Exposure", width: 14, cell: func(r row) string { return exposureLabel(r.exposure) }}

	cols := []column{port, proto}
	if showBinding {
		cols = append(cols, binding)
	}
	cols = append(cols, process, pid)
	if showExposure {
		cols = append(cols, exposure)
	}

	used := markerWidth + len(columnGap)*(len(cols)-1)
	for _, c := range cols {
		used += c.width
	}
	processWidth := w - used
	if showBinding && processWidth < minProcessWidth {
		shrink := min(minProcessWidth-processWidth, bindingWidth-minBindingWidth)
		processWidth += shrink
		for i := range cols {
			if cols[i].title == binding.title {
				cols[i].width -= shrink
			}
		}
	}
	for i := range cols {
		if cols[i].title == process.title {
			cols[i].width = max(processWidth, len("Process"))
		}
	}
	return cols
}

func (m model) renderTable() string {
	w := m.viewWidth()
	cols := m.columns()
	footer := m.tableFooter()

	lines := []string{m.statusLine(), boldStyle.Render(fit(tableLine("", cols, nil), w))}
	body := m.tableBody(cols, m.bodyHeightFor(len(footer)))
	lines = append(lines, body...)
	for range m.bodyHeightFor(len(footer)) - len(body) {
		lines = append(lines, "")
	}
	lines = append(lines, footer...)
	return strings.Join(lines, "\n")
}

// tableLine renders a header (r nil) or a row with the given marker.
func tableLine(marker string, cols []column, r *row) string {
	cells := make([]string, 0, len(cols))
	for _, c := range cols {
		text := c.title
		if r != nil {
			text = c.cell(*r)
		}
		cells = append(cells, pad(text, c.width))
	}
	return pad(marker, markerWidth) + strings.Join(cells, columnGap)
}

// tableBody renders the visible window of rows, or the empty state.
func (m model) tableBody(cols []column, height int) []string {
	w := m.viewWidth()
	switch {
	case len(m.visible) > 0:
	case !m.loaded && m.listErr != "":
		return []string{fit("No data yet.", w)}
	case !m.loaded:
		return []string{fit("Loading local ports…", w)}
	case len(m.all) == 0:
		return []string{fit("No listening or bound sockets found.", w)}
	default:
		return []string{fit(fmt.Sprintf("No sockets match %q.", m.search.Value()), w)}
	}

	offset := m.clampedOffset()
	end := min(offset+height, len(m.visible))
	lines := make([]string, 0, end-offset)
	for i := offset; i < end; i++ {
		r := m.visible[i]
		if i == m.cursor {
			lines = append(lines, selectedStyle.Render(fit(tableLine(">", cols, &r), w)))
			continue
		}
		lines = append(lines, fit(tableLine("", cols, &r), w))
	}
	return lines
}

// statusLine is the header: counts, sort, refresh time and refresh state.
func (m model) statusLine() string {
	parts := []string{"portpeek"}
	if m.loaded {
		parts = append(parts,
			fmt.Sprintf("%d/%d sockets", len(m.visible), len(m.all)),
			"sort: "+m.sort.String(),
			"refreshed "+m.refreshed.Format(timeLayout),
		)
	}
	if m.loading {
		parts = append(parts, "refreshing…")
	}
	if m.paused {
		parts = append(parts, "paused")
	}
	return boldStyle.Render(fit(strings.Join(parts, "  "), m.viewWidth()))
}

// tableFooter returns the lines under the table: search, errors and notices,
// the completeness hint, and key help.
func (m model) tableFooter() []string {
	w := m.viewWidth()
	lines := []string{}
	switch {
	case m.searching:
		lines = append(lines, lipgloss.NewStyle().MaxWidth(w).Render(m.search.View()))
	case m.search.Value() != "":
		lines = append(lines, fit(fmt.Sprintf("filter: %s  (esc clears)", m.search.Value()), w))
	}
	if m.listErr != "" {
		lines = append(lines, errorStyle.Render(fit(m.listErr, w)))
	}
	if m.notice != "" {
		style := boldStyle
		if m.noticeIsErr {
			style = errorStyle
		}
		lines = append(lines, style.Render(fit(m.notice, w)))
	}
	if m.loaded {
		if hint := cli.CompletenessHint(m.owners); hint != "" {
			lines = append(lines, faintStyle.Render(fit(hint, w)))
		}
	}

	k := m.keys
	help := helpLine(k.up, k.details, k.search, k.sort, k.refresh, k.pause, k.quit)
	if m.searching {
		help = "type to filter  enter keep  esc clear"
	}
	return append(lines, faintStyle.Render(fit(help, w)))
}

// bodyHeight is the number of table rows that fit on screen.
func (m model) bodyHeight() int {
	return m.bodyHeightFor(len(m.tableFooter()))
}

func (m model) bodyHeightFor(footerLines int) int {
	const headerLines = 2
	return max(m.viewHeight()-headerLines-footerLines, 1)
}

// clampedOffset returns the first visible row so that the cursor is on
// screen, scrolling as little as possible from the current offset.
func (m model) clampedOffset() int {
	h := m.bodyHeight()
	off := m.offset
	if m.cursor < off {
		off = m.cursor
	}
	if m.cursor >= off+h {
		off = m.cursor - h + 1
	}
	return clamp(off, 0, max(len(m.visible)-h, 0))
}

// renderDetails shows the CLI's answer for the selected port.
func (m model) renderDetails() string {
	w := m.viewWidth()
	d := m.detail
	q := d.query

	title := fmt.Sprintf("Port %d/%s", q.Port, q.Protocol)
	if d.loading {
		title += "  inspecting…"
	} else {
		title += "  inspected " + d.inspected.Format(timeLayout)
	}

	var body []string
	switch {
	case d.loading:
	case d.errText != "":
		body = append(body, errorStyle.Render(fit(d.errText, w)))
	default:
		text := strings.TrimSuffix(cli.RenderText(q, d.owners), "\n")
		for _, line := range strings.Split(text, "\n") {
			body = append(body, fit(line, w))
		}
	}
	body = append(body, "", fit(fmt.Sprintf("Copy: portpeek %d --%s", q.Port, q.Protocol), w))

	footer := []string{}
	if d.status != "" {
		footer = append(footer, boldStyle.Render(fit(d.status, w)))
	}
	footer = append(footer, faintStyle.Render(fit(helpLine(m.keys.stop, m.keys.back), w)))

	room := max(m.viewHeight()-2-len(footer), 0)
	if len(body) > room {
		body = body[:room]
	}
	lines := append([]string{boldStyle.Render(fit(title, w)), ""}, body...)
	for range room - len(body) {
		lines = append(lines, "")
	}
	lines = append(lines, footer...)
	return strings.Join(lines, "\n")
}

// helpLine renders bindings as "key: action" pairs.
func helpLine(bindings ...key.Binding) string {
	parts := make([]string, 0, len(bindings))
	for _, b := range bindings {
		h := b.Help()
		parts = append(parts, h.Key+": "+h.Desc)
	}
	return strings.Join(parts, "  ")
}

// fit truncates s to w display cells, ending in "…" when cut.
func fit(s string, w int) string {
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
	return b.String() + "…"
}

// pad fits s into exactly w display cells.
func pad(s string, w int) string {
	s = fit(s, w)
	return s + strings.Repeat(" ", max(w-lipgloss.Width(s), 0))
}
