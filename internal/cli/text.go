package cli

import (
	"fmt"
	"io"
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
)

// Styling is applied only when stdout is a colour-capable terminal; see
// StylingAllowed. lipgloss v2's Style.Render always emits escape codes and
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

	styled := tty && StylingAllowed(os.Getenv)
	if styled {
		// Turns on escape-code processing in the legacy Windows console; a
		// no-op elsewhere.
		lipgloss.EnableLegacyWindowsANSI(f)
	}
	return newTextView(width, styled)
}

// StylingAllowed reports whether the environment permits styled text on a
// terminal: NO_COLOR (https://no-color.org) must be unset or empty, and TERM
// must not be "dumb". The terminal interface follows the same rule.
func StylingAllowed(getenv func(string) string) bool {
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
	mixed := len(protocols(o.Sockets)) > 1
	lines = append(lines, v.align(indent, v.bindingRows(o.Sockets, mixed, false))...)

	// An unknown owner has no PID, and "kill 0" would signal the user's own
	// process group, so neither a command nor a stop line is printed; the
	// hint line says how to see more.
	if isUnknownOwner(p) {
		return lines
	}
	if p.Command != "" {
		lines = append(lines, v.line(indent, EscapeControls(shortCommand(p.Name, p.Command)), nil))
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

// detail is the --detail view. One owner is a single block led by the port;
// several owners get a "5353/udp  2 processes" headline and a block each,
// separated by blank lines. A block is the owner line, one line per binding
// and the cmd, cwd and stop lines. The owner line, bindings and command are
// cut to the view's width; the working directory and stop line are kept
// whole so they can be copied.
func (v textView) detail(q inspect.Query, owners []inspect.Owner) string {
	if len(owners) == 0 {
		return v.compact(q, owners)
	}

	all := []inspect.Socket{}
	for _, o := range owners {
		all = append(all, o.Sockets...)
	}
	mixed := len(protocols(all)) > 1
	several := len(owners) > 1

	blocks := []string{}
	if several {
		blocks = append(blocks, v.align("", [][]cell{{
			{text: portLabel(q, all), style: v.bold},
			plain(strconv.Itoa(len(owners)) + " processes"),
		}}, 2)[0])
	}
	for _, o := range owners {
		blocks = append(blocks, strings.Join(v.detailOwner(q, o, several, mixed), "\n"))
	}
	if hint := completenessHint(owners); hint != "" {
		blocks = append(blocks, v.line("", hint, v.dim))
	}
	return strings.Join(blocks, "\n\n") + "\n"
}

// Keys of the --detail process lines, padded to keyWidth columns.
const (
	keyWidth   = 6
	commandKey = "cmd"
	dirKey     = "cwd"
	stopKey    = "stop"
	// stopSeparator joins the manual stop command and the --stop one.
	stopSeparator = "  ·  "
)

// detailOwner is one owner's block in the --detail view. several says the
// port has more than one owner, so the port is left to the shared headline
// and the --stop command needs --pid; mixed says the bindings need their
// protocol.
func (v textView) detailOwner(q inspect.Query, o inspect.Owner, several, mixed bool) []string {
	p := o.Process
	lines := []string{v.detailHeadline(q, o, several)}
	lines = append(lines, v.align(indent, v.bindingRows(o.Sockets, mixed, true), 2, 2, 2)...)

	command := processField(p, inspect.FieldCommand, "")
	if p.Command != "" {
		command = fitCommand(p.Name, p.Command, v.width-len(indent)-keyWidth)
	}
	lines = append(lines,
		v.keyLine(commandKey, command),
		v.keyLine(dirKey, processField(p, inspect.FieldWorkingDir, p.WorkingDir)),
	)

	// An unknown owner has no PID, and "kill 0" would signal the user's own
	// process group, so it gets no stop line.
	if isUnknownOwner(p) {
		return lines
	}
	return append(lines, v.keyLine(stopKey, stopHint(p.PID)+stopSeparator+stopCommand(q, p.PID, several)))
}

// detailHeadline is the owner line of a --detail block: "node  PID 48213
// user kaanemec", led by the port when the port has a single owner. The user
// is left out when it is missing without a reason.
func (v textView) detailHeadline(q inspect.Query, o inspect.Owner, several bool) string {
	p := o.Process
	cells := []cell{}
	if !several {
		cells = append(cells, cell{text: portLabel(q, o.Sockets), style: v.bold})
	}
	name := ownerName(p)
	if p.Name == "" && !isUnknownOwner(p) {
		name = "name " + processField(p, inspect.FieldName, "")
	}
	cells = append(cells,
		cell{text: name, style: v.bold},
		cell{text: "PID " + PIDText(p.PID), style: v.dim},
	)
	if p.User != "" || p.Unavailable[inspect.FieldUser] != "" {
		cells = append(cells, cell{text: "user " + processField(p, inspect.FieldUser, p.User), style: v.dim})
	}
	return v.align("", [][]cell{cells}, 2, 2, 2)[0]
}

// keyLine is an indented process line with its key dim and padded to
// keyWidth: "  cwd   /Users/kaanemec/app". The value is not cut.
func (v textView) keyLine(key, value string) string {
	return indent + v.dim(key) + strings.Repeat(" ", max(keyWidth-len(key), 1)) + value
}

// fitCommand shortens a command line as the default view does, argv[0] to
// its base name, and fits it into w cells. A command that is too long keeps
// as many whole arguments as fit and ends in " …" and the count of the
// arguments left out: "node --a --b …  (+3 args)".
func fitCommand(name, command string, w int) string {
	argv0, rest := shortArgv(name, command)
	short := EscapeControls(argv0 + rest)
	if lipgloss.Width(short) <= w {
		return short
	}

	words := []string{EscapeControls(argv0)}
	for _, arg := range strings.FieldsFunc(rest, func(r rune) bool { return r == ' ' }) {
		words = append(words, EscapeControls(arg))
	}
	args := len(words) - 1
	for shown := args - 1; shown >= 0; shown-- {
		text := strings.Join(words[:shown+1], " ") + " …" + argsCount(args-shown)
		if lipgloss.Width(text) <= w {
			return text
		}
	}

	// Not even argv[0] fits beside the count, so argv[0] itself is cut.
	count := argsCount(args)
	if room := w - lipgloss.Width(count); room > 1 {
		return Truncate(strings.Join(words, " "), room) + count
	}
	return Truncate(short, w)
}

// argsCount is the suffix that counts the arguments a cut command left out,
// or "" when none were.
func argsCount(n int) string {
	switch n {
	case 0:
		return ""
	case 1:
		return "  (+1 arg)"
	default:
		return "  (+" + strconv.Itoa(n) + " args)"
	}
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

// bindingRows describes collapsed bindings, one row each: address, state and
// exposure, with the family as its own column in detail. The protocol leads
// the address when mixed is set.
func (v textView) bindingRows(sockets []inspect.Socket, mixed, detail bool) [][]cell {
	rows := [][]cell{}
	for _, b := range CollapseBindings(sockets) {
		row := []cell{plain(bindingText(b, mixed))}
		if detail {
			// The family column names both families, so the address goes
			// without the dual-stack marker.
			row = []cell{plain(withProtocol(b, b.hostPort(), mixed)), plain(b.familyLabel())}
		}
		rows = append(rows, append(row, plain(b.stateLabel()), plain(b.exposureWord())))
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

// ownerName is the process name with control characters escaped, "unknown
// process" for an owner the OS did not attribute, or "name unavailable" when
// only the name is missing.
func ownerName(p inspect.Process) string {
	switch {
	case isUnknownOwner(p):
		return "unknown process"
	case p.Name == "":
		return "name unavailable"
	default:
		return EscapeControls(p.Name)
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
