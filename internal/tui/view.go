package tui

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/kaanemec/portpeek/internal/cli"
	"github.com/kaanemec/portpeek/internal/inspect"
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

// Chrome of the styled screen.
const (
	badgeText = " portpeek "
	// minBoxWidth is the narrowest terminal that gets the details box.
	minBoxWidth = 8
	// boxChrome is the border and padding cells on each row of the box.
	boxChrome = 4
)

// View renders the current screen in the alternate screen buffer.
func (m model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	return v
}

// render returns the screen as exactly one line per terminal row, none
// wider than the terminal, so nothing wraps or scrolls.
func (m model) render() string {
	if m.screen == screenDetails {
		return clampLines(m.renderDetails(), m.viewHeight())
	}
	return clampLines(m.renderTable(), m.viewHeight())
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
	// style colours a cell; nil keeps the row's colour.
	style func(theme, row) lipgloss.Style
}

// columns lays out the table for the current width.
func (m model) columns() []column {
	w := m.viewWidth()
	showBinding := w >= bindingMinWidth
	showExposure := w >= exposureMinWidth

	bindingWidth := minBindingWidth
	for _, r := range m.visible {
		bindingWidth = max(bindingWidth, lipgloss.Width(r.bind.String()))
	}
	bindingWidth = min(bindingWidth, maxBindingWidth)

	port := column{
		title: "Port",
		width: 5,
		cell:  func(r row) string { return strconv.Itoa(r.bind.Port) },
		style: func(t theme, _ row) lipgloss.Style { return t.port },
	}
	proto := column{
		title: "Proto",
		width: 5,
		cell:  func(r row) string { return string(r.bind.Protocol) },
		style: func(t theme, r row) lipgloss.Style { return t.protoStyle(r.bind.Protocol) },
	}
	binding := column{title: "Binding", width: bindingWidth, cell: func(r row) string { return r.bind.String() }}
	process := column{
		title: "Process",
		cell:  func(r row) string { return r.process },
		style: func(t theme, r row) lipgloss.Style {
			if r.pid <= 0 {
				return t.unknown
			}
			return t.process
		},
	}
	pid := column{
		title: "PID",
		width: 7,
		cell:  func(r row) string { return cli.PIDText(r.pid) },
		style: func(t theme, _ row) lipgloss.Style { return t.dim },
	}
	exposure := column{
		title: "Exposure",
		width: 14,
		cell:  func(r row) string { return cli.ExposureLabel(r.bind.Exposure()) },
		style: func(t theme, r row) lipgloss.Style { return t.exposureStyle(r.bind.Exposure()) },
	}

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
	t := m.theme
	w := m.viewWidth()
	cols := m.columns()
	footer := m.tableFooter()

	header := t.bar(w, t.header, tableSegs(t, "", cols, nil)...)
	lines := []string{m.statusLine(), header}
	body := m.tableBody(cols, m.bodyHeightFor(len(footer)))
	lines = append(lines, body...)
	for range m.bodyHeightFor(len(footer)) - len(body) {
		lines = append(lines, "")
	}
	lines = append(lines, footer...)
	return strings.Join(lines, "\n")
}

// tableSegs lays out a header (r nil) or a row with the given marker as
// padded cells and gaps, each in its column's style.
func tableSegs(t theme, marker string, cols []column, r *row) []seg {
	segs := make([]seg, 0, 2*len(cols)+1)
	segs = append(segs, seg{text: pad(marker, markerWidth), style: t.key})
	for i, c := range cols {
		if i > 0 {
			segs = append(segs, plainSeg(columnGap))
		}
		if r == nil {
			segs = append(segs, plainSeg(pad(c.title, c.width)))
			continue
		}
		s := plainSeg(pad(c.cell(*r), c.width))
		if c.style != nil {
			s.style = c.style(t, *r)
		}
		segs = append(segs, s)
	}
	return segs
}

// tableBody renders the visible window of rows, or the empty state.
func (m model) tableBody(cols []column, height int) []string {
	t := m.theme
	w := m.viewWidth()
	empty := ""
	switch {
	case len(m.visible) > 0:
	case !m.loaded && m.listErr != "":
		empty = "No data yet."
	case !m.loaded:
		empty = "Loading local ports…"
	case len(m.all) == 0:
		empty = "No listening or bound sockets found."
	default:
		empty = fmt.Sprintf("No sockets match %q.", m.search.Value())
	}
	if empty != "" {
		return []string{t.line(w, seg{text: empty, style: t.hint})}
	}

	offset := m.clampedOffset()
	end := min(offset+height, len(m.visible))
	lines := make([]string, 0, end-offset)
	for i := offset; i < end; i++ {
		r := m.visible[i]
		if i == m.cursor {
			lines = append(lines, t.bar(w, t.selected, tableSegs(t, t.marker, cols, &r)...))
			continue
		}
		lines = append(lines, t.line(w, tableSegs(t, "", cols, &r)...))
	}
	return lines
}

// statusLine is the title bar: counts, sort, refresh time and refresh
// state, and when styled the kept filter.
func (m model) statusLine() string {
	t := m.theme
	if !t.styled {
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
		return fit(strings.Join(parts, "  "), m.viewWidth())
	}

	segs := []seg{{text: badgeText, style: t.badge}}
	add := func(s ...seg) {
		segs = append(segs, plainSeg("  "))
		segs = append(segs, s...)
	}
	if m.loaded {
		add(
			seg{text: fmt.Sprintf("%d/%d", len(m.visible), len(m.all)), style: t.strong},
			seg{text: " sockets", style: t.dim},
		)
		add(seg{text: "sort: ", style: t.dim}, seg{text: m.sort.String(), style: t.key})
		add(seg{text: "refreshed ", style: t.dim}, plainSeg(m.refreshed.Format(timeLayout)))
	}
	if m.loading {
		add(seg{text: m.spinner.View() + " ", style: t.key}, seg{text: "refreshing…", style: t.dim})
	}
	if m.paused {
		add(seg{text: "⏸ paused", style: t.warn.Bold(true)})
	}
	if !m.searching && m.search.Value() != "" {
		add(seg{text: "filter: ", style: t.dim}, seg{text: m.search.Value(), style: t.key})
	}
	return t.line(m.viewWidth(), segs...)
}

// tableFooter returns the lines under the table: search, errors and notices,
// the completeness hint, and key help.
func (m model) tableFooter() []string {
	t := m.theme
	w := m.viewWidth()
	lines := []string{}
	switch {
	case m.searching:
		lines = append(lines, lipgloss.NewStyle().MaxWidth(w).Render(m.search.View()))
	case m.search.Value() != "" && !t.styled:
		// Styled, the title bar shows the kept filter.
		lines = append(lines, fit(fmt.Sprintf("filter: %s  (esc clears)", m.search.Value()), w))
	}
	if m.listErr != "" {
		lines = append(lines, t.line(w, seg{text: m.listErr, style: t.err}))
	}
	if m.notice != "" {
		lines = append(lines, t.line(w, seg{text: m.notice, style: t.toneStyle(m.noticeTone)}))
	}
	if m.loaded {
		if hint := cli.CompletenessHint(m.owners); hint != "" {
			lines = append(lines, t.line(w, seg{text: hint, style: t.hint}))
		}
	}

	if m.searching {
		if !t.styled {
			return append(lines, fit("type to filter  enter keep  esc clear", w))
		}
		return append(lines, t.help(w, key.Help{Key: "esc", Desc: "clear"}, key.Help{Key: "enter", Desc: "keep"}))
	}
	k := m.keys
	return append(lines, t.help(w, helps(k.up, k.details, k.search, k.sort, k.refresh, k.pause, k.quit)...))
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

// renderDetails shows the CLI's --detail answer for the selected port, laid
// out for the pane's width, in a box when styled. The title bar names the
// port, so the box has no title.
func (m model) renderDetails() string {
	t := m.theme
	w := m.viewWidth()
	d := m.detail
	q := d.query

	boxed := t.styled && w >= minBoxWidth
	inner := w
	if boxed {
		inner = w - boxChrome
	}

	var body []string
	switch {
	case d.loading:
	case d.errText != "":
		body = append(body, t.line(inner, seg{text: d.errText, style: t.err}))
	default:
		text := strings.TrimSuffix(cli.RenderDetail(q, d.owners, inner), "\n")
		body = append(body, t.detailLines(inner, text, cli.CompletenessHint(d.owners))...)
	}

	footer := []string{}
	if d.status != "" {
		footer = append(footer, m.detailStatus())
	}
	footer = append(footer, t.help(w, helps(m.keys.stop, m.keys.back)...))

	room := max(m.viewHeight()-2-len(footer), 0)
	lines := []string{m.detailTitle()}
	if boxed {
		lines = append(lines, t.box(w, room, body)...)
	} else {
		if len(body) > room {
			body = body[:room]
		}
		lines = append(lines, "")
		lines = append(lines, body...)
		for range room - len(body) {
			lines = append(lines, "")
		}
	}
	lines = append(lines, footer...)
	return strings.Join(lines, "\n")
}

// detailTitle is the first line of the details screen:
// "portpeek  3000/tcp  inspected 12:01:00".
func (m model) detailTitle() string {
	t := m.theme
	d := m.detail
	q := d.query
	port := fmt.Sprintf("%d/%s", q.Port, q.Protocol)
	if !t.styled {
		title := "portpeek  " + port + "  inspected " + d.inspected.Format(timeLayout)
		if d.loading {
			title = "portpeek  " + port + "  inspecting…"
		}
		return fit(title, m.viewWidth())
	}

	segs := []seg{{text: badgeText, style: t.badge}, plainSeg("  "), {text: port, style: t.strong}, plainSeg("  ")}
	if d.loading {
		segs = append(segs, seg{text: m.spinner.View() + " ", style: t.key}, seg{text: "inspecting…", style: t.dim})
	} else {
		segs = append(segs, seg{text: "inspected ", style: t.dim}, plainSeg(d.inspected.Format(timeLayout)))
	}
	return t.line(m.viewWidth(), segs...)
}

// detailStatus is the line above the details footer: the stop prompt as a
// warning bar, progress, or why nothing will be stopped.
func (m model) detailStatus() string {
	t := m.theme
	d := m.detail
	w := m.viewWidth()
	switch {
	case d.confirm && t.styled:
		return t.bar(w, t.confirm, plainSeg(" "+d.status))
	case d.stopping:
		return t.line(w, seg{text: d.status, style: t.key})
	default:
		return t.line(w, seg{text: d.status, style: t.warn})
	}
}

// box frames body in a rounded border of w cells and height+1 rows; the
// bottom edge is the last of those rows. Body lines are at most w-boxChrome
// cells; extra lines are dropped and missing ones left blank.
func (t theme) box(w, height int, body []string) []string {
	inner := w - boxChrome
	rule := strings.Repeat("─", max(w-2, 0))
	lines := []string{t.render(t.border, "╭"+rule+"╮")}

	rows := max(height-1, 0)
	edge := t.render(t.border, "│")
	for i := range rows {
		content := ""
		if i < len(body) {
			content = body[i]
		}
		fill := strings.Repeat(" ", max(inner-lipgloss.Width(content), 0))
		lines = append(lines, edge+" "+content+fill+" "+edge)
	}
	if height > 0 {
		lines = append(lines, t.render(t.border, "╰"+rule+"╯"))
	}
	return lines
}

// Keys of the process lines of cli.RenderDetail, as they start an indented
// line, padded to the value column.
var detailKeys = []string{"cmd   ", "cwd   ", "stop  "}

// detailIndent is the indent of every line inside an owner's block.
const detailIndent = "  "

// stopSeparator is what joins the two stop commands on the stop line.
const stopSeparator = "  ·  "

// detailLines fits each line of the CLI's detail text to w cells and styles
// it by what it is: the headline, an owner line, a binding, a process line,
// or the completeness hint. The wording is left as the CLI wrote it.
func (t theme) detailLines(w int, text, hint string) []string {
	lines := []string{}
	for i, line := range strings.Split(text, "\n") {
		var segs []seg
		switch {
		case !t.styled || line == "":
			segs = []seg{plainSeg(line)}
		case hint != "" && line == hint:
			segs = []seg{{text: line, style: t.hint}}
		case i == 0:
			segs = t.headlineSegs(line)
		case !strings.HasPrefix(line, detailIndent):
			segs = t.ownerSegs(line)
		default:
			segs = t.blockSegs(line)
		}
		lines = append(lines, t.line(w, segs...))
	}
	return lines
}

// headlineSegs styles the first line: the port in the accent colour, then
// "2 processes" dim or the single owner as ownerSegs styles it. A line that
// does not start with a port, such as the no-match answer, stays plain.
func (t theme) headlineSegs(line string) []seg {
	port, rest, _ := strings.Cut(line, "  ")
	if !isPortLabel(port) {
		return []seg{plainSeg(line)}
	}
	segs := []seg{{text: port, style: t.port}, plainSeg("  ")}
	if strings.HasSuffix(rest, " processes") {
		return append(segs, seg{text: rest, style: t.dim})
	}
	return append(segs, t.ownerSegs(rest)...)
}

// ownerSegs styles "node  PID 48213  user kaanemec": the name bold, or
// muted for an unknown owner, and the PID and user dim.
func (t theme) ownerSegs(line string) []seg {
	i := strings.LastIndex(line, "  PID ")
	if i < 0 {
		return []seg{plainSeg(line)}
	}
	name := seg{text: line[:i], style: t.process}
	if name.text == "unknown process" || strings.HasPrefix(name.text, "name unavailable") {
		name.style = t.unknown
	}
	return []seg{name, {text: line[i:], style: t.dim}}
}

// isPortLabel reports whether s reads like "3000" or "3000/tcp+udp".
func isPortLabel(s string) bool {
	port, _, _ := strings.Cut(s, "/")
	_, err := strconv.Atoi(port)
	return err == nil
}

// blockSegs styles an indented line of an owner's block: a process line has
// its key dim, an unavailable value muted and the stop separator dim; any
// other line is a binding, whose exposure is coloured by risk.
func (t theme) blockSegs(line string) []seg {
	rest := strings.TrimPrefix(line, detailIndent)
	for _, key := range detailKeys {
		value, ok := strings.CutPrefix(rest, key)
		if !ok {
			continue
		}
		segs := []seg{plainSeg(detailIndent), {text: key, style: t.dim}}
		switch {
		case strings.HasPrefix(value, "unavailable"):
			return append(segs, seg{text: value, style: t.unknown})
		case strings.TrimSpace(key) == "stop":
			if manual, verified, ok := strings.Cut(value, stopSeparator); ok {
				return append(segs, plainSeg(manual), seg{text: stopSeparator, style: t.dim}, plainSeg(verified))
			}
		}
		return append(segs, plainSeg(value))
	}
	return t.bindingSegs(line)
}

// bindingSegs colours the exposure that ends a binding line by risk:
// "  127.0.0.1:3000  v4  listening  loopback only".
func (t theme) bindingSegs(line string) []seg {
	for _, e := range []inspect.Exposure{
		inspect.ExposureAllInterfaces,
		inspect.ExposureLoopback,
		inspect.ExposureInterface,
		inspect.ExposureUnknown,
	} {
		i := strings.LastIndex(line, "  "+cli.ExposureLabel(e))
		if i < 0 {
			continue
		}
		i += len("  ")
		return []seg{plainSeg(line[:i]), {text: line[i:], style: t.exposureStyle(e)}}
	}
	return []seg{plainSeg(line)}
}

// helps returns the help text of bindings.
func helps(bindings ...key.Binding) []key.Help {
	hs := make([]key.Help, 0, len(bindings))
	for _, b := range bindings {
		hs = append(hs, b.Help())
	}
	return hs
}

// help renders key hints in one line of at most w cells: "key: action"
// pairs when plain, the key in the accent colour and the action dim when
// styled.
func (t theme) help(w int, hs ...key.Help) string {
	if !t.styled {
		parts := make([]string, 0, len(hs))
		for _, h := range hs {
			parts = append(parts, h.Key+": "+h.Desc)
		}
		return fit(strings.Join(parts, "  "), w)
	}
	segs := make([]seg, 0, 3*len(hs))
	for i, h := range hs {
		if i > 0 {
			segs = append(segs, plainSeg("   "))
		}
		segs = append(segs, seg{text: strings.ToLower(h.Key), style: t.key}, seg{text: " " + h.Desc, style: t.dim})
	}
	return t.line(w, segs...)
}

// toneStyle is the colour of a stop outcome: green when the process exited,
// amber when it is still running, red when nothing was sent.
func (t theme) toneStyle(tn tone) lipgloss.Style {
	switch tn {
	case toneOK:
		return t.ok
	case toneWarn:
		return t.warn
	default:
		return t.err
	}
}

// fit truncates s to w display cells, ending in "…" when cut. Control
// characters are escaped first: a tab or escape sequence in a process name or
// command would otherwise shift every following column or reach the
// terminal.
func fit(s string, w int) string {
	return cli.Truncate(cli.EscapeControls(s), w)
}

// clampLines keeps the first height lines of s, so that a terminal too short
// for the header and footer never scrolls.
func clampLines(s string, height int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= height {
		return s
	}
	return strings.Join(lines[:max(height, 1)], "\n")
}

// pad fits s into exactly w display cells.
func pad(s string, w int) string {
	s = fit(s, w)
	return s + strings.Repeat(" ", max(w-lipgloss.Width(s), 0))
}
