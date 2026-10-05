package cli

import (
	"fmt"
	"io"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"golang.org/x/term"

	"github.com/kaanemec/portpeek/internal/inspect"
)

// Text layout.
const (
	// defaultWidth is the line width when stdout is not a terminal.
	defaultWidth = 100
	// minWidth is the narrowest layout; narrower terminals wrap.
	minWidth = 40

	indent    = "  "
	columnGap = 3
	// maxNameWidth caps the process name column of the several-owner table.
	maxNameWidth = 24
	// minBindingsWidth is the least room the table keeps for bindings.
	minBindingsWidth = 12
	// stopColumn is where the --stop alternative starts in the Stop section.
	stopColumn = 20
)

// Styling is applied only when stdout is a colour-capable terminal; see
// stylingAllowed. lipgloss v2's Style.Render always emits escape codes and
// leaves downsampling to a colorprofile writer, so the decision is made here
// rather than left to the library.
var (
	boldStyle = lipgloss.NewStyle().Bold(true)
	dimStyle  = lipgloss.NewStyle().Faint(true)
)

// textView renders results as text at a fixed width, styled or plain.
type textView struct {
	width  int
	styled bool
}

// newTextView returns a view for width columns, never narrower than minWidth.
func newTextView(width int, styled bool) textView {
	return textView{width: max(width, minWidth), styled: styled}
}

// plainView is the view the exported renderers use: the width of piped
// output and no styling, so callers such as the terminal interface can fit
// and style the lines themselves.
var plainView = newTextView(defaultWidth, false)

// stdoutView returns the view for out.stdout: the terminal's width and
// styling when stdout is a terminal, otherwise defaultWidth and plain text.
// A positive out.width overrides the width.
func stdoutView(out stdio) textView {
	width := defaultWidth
	f, ok := out.stdout.(*os.File)
	tty := ok && term.IsTerminal(int(f.Fd()))
	if tty {
		if w, _, err := term.GetSize(int(f.Fd())); err == nil && w > 0 {
			width = w
		}
	}
	if out.width > 0 {
		width = out.width
	}

	styled := tty && stylingAllowed(os.Getenv)
	if styled {
		// Turns on escape-code processing in the legacy Windows console; a
		// no-op elsewhere.
		lipgloss.EnableLegacyWindowsANSI(f)
	}
	return newTextView(width, styled)
}

// stylingAllowed reports whether the environment permits bold and dim text on
// a terminal: NO_COLOR (https://no-color.org) must be unset or empty, and
// TERM must not be "dumb".
func stylingAllowed(getenv func(string) string) bool {
	return getenv("NO_COLOR") == "" && getenv("TERM") != "dumb"
}

func (v textView) bold(s string) string {
	if !v.styled || s == "" {
		return s
	}
	return boldStyle.Render(s)
}

func (v textView) dim(s string) string {
	if !v.styled || s == "" {
		return s
	}
	return dimStyle.Render(s)
}

// unlimited returns v without a width limit, for --detail, where nothing is
// cut and the terminal wraps long lines.
func (v textView) unlimited() textView {
	v.width = math.MaxInt
	return v
}

// write writes the default or detail view of a result to w.
func (v textView) write(w io.Writer, q inspect.Query, owners []inspect.Owner, detail bool) error {
	text := v.compact(q, owners)
	if detail {
		text = v.detail(q, owners)
	}
	_, err := io.WriteString(w, text)
	return err
}

// cell is one column of an aligned line.
type cell struct {
	text string
	// style styles the text after it has been padded and cut; nil is plain.
	style func(string) string
}

func plain(text string) cell { return cell{text: text} }

// align lays out rows as aligned columns after prefix. Every column but the
// last is padded to its widest cell and followed by gaps[i] spaces
// (columnGap when gaps is short). Each line is cut to the view's width with
// "…"; styling is applied after cutting so escape codes are never split.
func (v textView) align(prefix string, rows [][]cell, gaps ...int) []string {
	widths := []int{}
	for _, row := range rows {
		for i, c := range row {
			if i == len(widths) {
				widths = append(widths, 0)
			}
			widths[i] = max(widths[i], lipgloss.Width(c.text))
		}
	}

	lines := make([]string, 0, len(rows))
	for _, row := range rows {
		var b strings.Builder
		b.WriteString(prefix)
		used := lipgloss.Width(prefix)
		for i, c := range row {
			text := Truncate(c.text, v.width-used)
			if c.style != nil {
				b.WriteString(c.style(text))
			} else {
				b.WriteString(text)
			}
			used += lipgloss.Width(text)

			gap := columnGap
			if i < len(gaps) {
				gap = gaps[i]
			}
			pad := widths[i] - lipgloss.Width(text) + gap
			last := i == len(row)-1
			if last || text != c.text || used+pad >= v.width {
				break
			}
			b.WriteString(strings.Repeat(" ", pad))
			used += pad
		}
		lines = append(lines, strings.TrimRight(b.String(), " "))
	}
	return lines
}

// line cuts one line of text to the view's width and styles it.
func (v textView) line(prefix, text string, style func(string) string) string {
	text = Truncate(text, v.width-lipgloss.Width(prefix))
	if style != nil {
		text = style(text)
	}
	return prefix + text
}

// compact is the default view: a headline per result and a few short lines.
func (v textView) compact(q inspect.Query, owners []inspect.Owner) string {
	lines := []string{}
	switch len(owners) {
	case 0:
		lines = append(lines, v.line("", noMatchText(q), nil))
	case 1:
		lines = append(lines, v.compactOwner(q, owners[0])...)
	default:
		lines = append(lines, v.compactTable(q, owners)...)
	}
	if hint := completenessHint(owners); hint != "" {
		lines = append(lines, v.line("", hint, v.dim))
	}
	return strings.Join(lines, "\n") + "\n"
}

// compactOwner is the default view of a single owner: headline, bindings,
// the shortened command and the stop command.
func (v textView) compactOwner(q inspect.Query, o inspect.Owner) []string {
	p := o.Process
	lines := []string{v.ownerHeadline(q, o)}
	lines = append(lines, v.align(indent, v.bindingRows(o.Sockets, false))...)

	// An unknown owner has no PID, and "kill 0" would signal the user's own
	// process group, so neither a command nor a stop line is printed; the
	// hint line says how to see more.
	if isUnknownOwner(p) {
		return lines
	}
	if p.Command != "" {
		lines = append(lines, v.line(indent, shortCommand(p.Name, p.Command), nil))
	} else {
		lines = append(lines, v.line(indent, "command "+processField(p, inspect.FieldCommand, ""), v.dim))
	}
	return append(lines, v.align(indent, [][]cell{{
		{text: "stop:", style: v.dim},
		plain(stopHint(p.PID)),
	}}, 1)...)
}

// compactTable is the default view of several owners: one aligned row each
// with name, PID, bindings and exposure.
func (v textView) compactTable(q inspect.Query, owners []inspect.Owner) []string {
	all := []inspect.Socket{}
	for _, o := range owners {
		all = append(all, o.Sockets...)
	}
	mixed := len(protocols(all)) > 1

	type tableRow struct{ name, pid, bindings, exposure string }
	rows := make([]tableRow, 0, len(owners))
	nameWidth, pidWidth, exposureWidth := 0, 0, 0
	for _, o := range owners {
		bindings := CollapseBindings(o.Sockets)
		texts := make([]string, 0, len(bindings))
		exposures := []string{}
		for _, b := range bindings {
			texts = append(texts, bindingText(b, mixed))
			if word := b.exposureWord(); !slices.Contains(exposures, word) {
				exposures = append(exposures, word)
			}
		}
		r := tableRow{
			name:     Truncate(ownerName(o.Process), maxNameWidth),
			pid:      "PID " + PIDText(o.Process.PID),
			bindings: strings.Join(texts, ", "),
			exposure: strings.Join(exposures, ", "),
		}
		if isUnknownOwner(o.Process) {
			r.pid = "-"
		}
		nameWidth = max(nameWidth, lipgloss.Width(r.name))
		pidWidth = max(pidWidth, lipgloss.Width(r.pid))
		exposureWidth = max(exposureWidth, lipgloss.Width(r.exposure))
		rows = append(rows, r)
	}

	// Bindings give way first, so the exposure column stays readable.
	const pidGap = 2
	others := len(indent) + nameWidth + columnGap + pidWidth + pidGap + columnGap + exposureWidth
	bindingsWidth := max(v.width-others, minBindingsWidth)

	cells := make([][]cell, 0, len(rows))
	for _, r := range rows {
		cells = append(cells, []cell{
			{text: r.name, style: v.bold},
			plain(r.pid),
			plain(Truncate(r.bindings, bindingsWidth)),
			plain(r.exposure),
		})
	}

	headline := v.align("", [][]cell{{
		{text: portLabel(q, all), style: v.bold},
		plain(strconv.Itoa(len(owners)) + " processes"),
	}}, 2)
	return append(headline, v.align(indent, cells, columnGap, pidGap)...)
}

// detail is the --detail view: every owner with its sockets, process details
// and stop commands in labelled sections. Nothing is cut.
func (v textView) detail(q inspect.Query, owners []inspect.Owner) string {
	if len(owners) == 0 {
		return v.compact(q, owners)
	}

	u := v.unlimited()
	blocks := []string{}
	if len(owners) > 1 {
		all := []inspect.Socket{}
		for _, o := range owners {
			all = append(all, o.Sockets...)
		}
		blocks = append(blocks, u.align("", [][]cell{{
			{text: portLabel(q, all), style: v.bold},
			plain(strconv.Itoa(len(owners)) + " processes"),
		}}, 2)[0])
	}
	for _, o := range owners {
		blocks = append(blocks, strings.Join(u.detailOwner(q, o, len(owners) > 1), "\n"))
	}
	if hint := completenessHint(owners); hint != "" {
		blocks = append(blocks, indent+v.dim(hint))
	}
	return strings.Join(blocks, "\n\n") + "\n"
}

// detailOwner is one owner's block in the --detail view. several says the
// port has more than one owner, so the --stop command needs --pid.
func (v textView) detailOwner(q inspect.Query, o inspect.Owner, several bool) []string {
	const sectionIndent = indent + indent
	p := o.Process

	lines := []string{v.ownerHeadline(q, o), "", indent + v.bold("Sockets")}
	sockets := v.bindingRows(o.Sockets, true)
	lines = append(lines, v.align(sectionIndent, sockets)...)

	lines = append(lines, "", indent+v.bold("Process"))
	fields := [][]cell{}
	if p.Name == "" {
		fields = append(fields, v.field("name", processField(p, inspect.FieldName, "")))
	}
	fields = append(fields,
		v.field("user", processField(p, inspect.FieldUser, p.User)),
		v.field("command", processField(p, inspect.FieldCommand, p.Command)),
		v.field("working dir", processField(p, inspect.FieldWorkingDir, p.WorkingDir)),
	)
	lines = append(lines, v.align(sectionIndent, fields)...)

	if isUnknownOwner(p) {
		return lines
	}
	hint := stopHint(p.PID)
	pad := strings.Repeat(" ", max(stopColumn-lipgloss.Width(hint), columnGap))
	return append(lines,
		"",
		indent+v.bold("Stop"),
		sectionIndent+hint+pad+v.dim("or:")+" "+stopCommand(q, p.PID, several),
	)
}

// field is a labelled row of the Process section.
func (v textView) field(label, value string) []cell {
	return []cell{{text: label, style: v.dim}, plain(value)}
}

// ownerHeadline names the port and its owner: "3000/tcp  node  (PID 48213)".
func (v textView) ownerHeadline(q inspect.Query, o inspect.Owner) string {
	p := o.Process
	cells := []cell{
		{text: portLabel(q, o.Sockets), style: v.bold},
		{text: ownerName(p), style: v.bold},
	}
	if !isUnknownOwner(p) {
		cells = append(cells, plain("(PID "+strconv.Itoa(p.PID)+")"))
	}
	return v.align("", [][]cell{cells}, 2, 2)[0]
}

// bindingRows describes an owner's collapsed bindings, one row each: address,
// state and exposure, plus the family and an exposure note in detail. The
// protocol leads the address when the owner holds more than one.
func (v textView) bindingRows(sockets []inspect.Socket, detail bool) [][]cell {
	mixed := len(protocols(sockets)) > 1
	rows := [][]cell{}
	for _, b := range CollapseBindings(sockets) {
		exposure := b.exposureWord()
		row := []cell{plain(bindingText(b, mixed))}
		if detail {
			// The family column says IPv4+IPv6, so the address goes without
			// the dual-stack marker.
			row = []cell{plain(withProtocol(b, b.hostPort(), mixed)), plain(b.familyLabel())}
			if note := exposureNote(b.Exposure()); note != "" {
				exposure += " " + note
			}
		}
		rows = append(rows, append(row, plain(b.stateLabel()), plain(exposure)))
	}
	return rows
}

// bindingText is the binding, led by its protocol when mixed is set.
func bindingText(b Binding, mixed bool) string {
	return withProtocol(b, b.String(), mixed)
}

// withProtocol prefixes text with the binding's protocol when mixed is set.
func withProtocol(b Binding, text string, mixed bool) string {
	if mixed {
		return string(b.Protocol) + " " + text
	}
	return text
}

// portLabel is the port with the protocols of sockets, "3000/tcp+udp", or
// with the queried protocol when there are no sockets.
func portLabel(q inspect.Query, sockets []inspect.Socket) string {
	protos := protocols(sockets)
	if len(protos) == 0 && q.Protocol != "" {
		protos = []string{string(q.Protocol)}
	}
	if len(protos) == 0 {
		return strconv.Itoa(q.Port)
	}
	return strconv.Itoa(q.Port) + "/" + strings.Join(protos, "+")
}

// ownerName is the process name, "unknown process" for an owner the OS did
// not attribute, or "name unavailable" when only the name is missing.
func ownerName(p inspect.Process) string {
	switch {
	case isUnknownOwner(p):
		return "unknown process"
	case p.Name == "":
		return "name unavailable"
	default:
		return p.Name
	}
}

// stopCommand is the portpeek command that stops pid through the verified
// --stop flow, naming the PID when the port has several owners.
func stopCommand(q inspect.Query, pid int, several bool) string {
	parts := []string{"portpeek", strconv.Itoa(q.Port), "--stop"}
	if several {
		parts = append(parts, "--pid", strconv.Itoa(pid))
	}
	if q.Protocol != "" {
		parts = append(parts, "--"+string(q.Protocol))
	}
	return strings.Join(parts, " ")
}

// noMatchText is the answer when nothing holds the port.
func noMatchText(q inspect.Query) string {
	return fmt.Sprintf("no listening or bound socket on %d (%s)", q.Port, protocolPhrase(q))
}
