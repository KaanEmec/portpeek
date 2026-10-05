package tui

import (
	"image/color"
	"strings"

	"charm.land/bubbles/v2/textinput"
	"charm.land/lipgloss/v2"

	"github.com/kaanemec/portpeek/internal/cli"
	"github.com/kaanemec/portpeek/internal/inspect"
)

// Selection markers. Styled, the highlighted bar shows the selection and the
// marker only repeats it; plain, the marker is all there is.
const (
	plainMarker  = ">"
	styledMarker = "❯"
)

// palette is one pair of colours per role, for light and dark backgrounds.
// Every colour is an index of the 256-colour palette, so the screen looks
// the same on a 256-colour terminal such as macOS Terminal and on a
// true-colour one; Bubble Tea downsamples further where needed.
type palette struct {
	accent, onAccent         color.Color
	text, muted              color.Color
	green, amber, blue, pink color.Color
	red, onRed, redBg        color.Color
	headerBg, selectedBg     color.Color
	selectedText             color.Color
}

func newPalette(isDark bool) palette {
	ld := lipgloss.LightDark(isDark)
	c := lipgloss.Color
	return palette{
		accent:       ld(c("30"), c("80")),
		onAccent:     ld(c("231"), c("16")),
		text:         ld(c("236"), c("254")),
		muted:        ld(c("243"), c("245")),
		green:        ld(c("28"), c("114")),
		amber:        ld(c("130"), c("215")),
		blue:         ld(c("25"), c("75")),
		pink:         ld(c("127"), c("177")),
		red:          ld(c("160"), c("203")),
		onRed:        c("231"),
		redBg:        ld(c("160"), c("124")),
		headerBg:     ld(c("254"), c("236")),
		selectedBg:   ld(c("152"), c("23")),
		selectedText: ld(c("16"), c("231")),
	}
}

// theme holds every style of the interface. Built unstyled, every style is
// a no-op and the screen is plain text with a ">" marker, exactly as wide as
// the styled one.
type theme struct {
	styled bool
	marker string
	// selectedBg is laid under every cell of the selected row.
	selectedBg color.Color

	badge, port, tcp, udp, process, unknown lipgloss.Style
	header, selected, dim, hint, key        lipgloss.Style
	ok, warn, err, heading, border, confirm lipgloss.Style
	strong                                  lipgloss.Style
	exposure                                map[inspect.Exposure]lipgloss.Style
}

// newTheme returns the styled theme for a dark or light background, or the
// plain one when styled is false.
func newTheme(styled, isDark bool) theme {
	if !styled {
		return theme{marker: plainMarker}
	}
	p := newPalette(isDark)
	s := lipgloss.NewStyle
	return theme{
		styled:     true,
		marker:     styledMarker,
		selectedBg: p.selectedBg,

		badge:   s().Bold(true).Foreground(p.onAccent).Background(p.accent),
		port:    s().Bold(true).Foreground(p.accent),
		tcp:     s().Foreground(p.blue),
		udp:     s().Foreground(p.pink),
		process: s().Bold(true),
		unknown: s().Italic(true).Foreground(p.muted),

		header:   s().Bold(true).Foreground(p.muted).Background(p.headerBg),
		selected: s().Bold(true).Foreground(p.selectedText).Background(p.selectedBg),
		dim:      s().Foreground(p.muted),
		hint:     s().Italic(true).Foreground(p.muted),
		key:      s().Bold(true).Foreground(p.accent),

		ok:      s().Foreground(p.green),
		warn:    s().Foreground(p.amber),
		err:     s().Foreground(p.red),
		heading: s().Bold(true).Foreground(p.accent),
		border:  s().Foreground(p.accent),
		confirm: s().Bold(true).Foreground(p.onRed).Background(p.redBg),
		strong:  s().Bold(true).Foreground(p.text),
		exposure: map[inspect.Exposure]lipgloss.Style{
			inspect.ExposureLoopback:      s().Foreground(p.green),
			inspect.ExposureAllInterfaces: s().Foreground(p.amber),
			inspect.ExposureInterface:     s().Foreground(p.blue),
			inspect.ExposureUnknown:       s().Foreground(p.muted),
		},
	}
}

// exposureStyle is the colour of an exposure: green for loopback only,
// amber for all interfaces, blue for one interface, grey when unknown.
func (t theme) exposureStyle(e inspect.Exposure) lipgloss.Style {
	if s, ok := t.exposure[e]; ok {
		return s
	}
	return t.exposure[inspect.ExposureUnknown]
}

// protoStyle colours a protocol tag: tcp blue, udp magenta.
func (t theme) protoStyle(p inspect.Protocol) lipgloss.Style {
	if p == inspect.UDP {
		return t.udp
	}
	return t.tcp
}

// searchStyles styles the search input: the prompt in the accent colour and
// the placeholder dim. Plain, the input keeps its defaults.
func (t theme) searchStyles(isDark bool) textinput.Styles {
	s := textinput.DefaultStyles(isDark)
	if !t.styled {
		return s
	}
	for _, state := range []*textinput.StyleState{&s.Focused, &s.Blurred} {
		state.Prompt = t.key
		state.Placeholder = t.hint
	}
	s.Cursor.Color = newPalette(isDark).accent
	return s
}

// seg is a piece of a line and the style it is drawn in.
type seg struct {
	text  string
	style lipgloss.Style
}

func plainSeg(text string) seg { return seg{text: text} }

// line joins segs into one line of at most w cells and styles each piece
// that survives the cut. Control characters are escaped first and styling
// comes last, so neither a process name nor the cut can split an escape
// code. Unstyled, the result is fit of the joined text.
func (t theme) line(w int, segs ...seg) string {
	var b strings.Builder
	for i := range segs {
		segs[i].text = cli.EscapeControls(segs[i].text)
		b.WriteString(segs[i].text)
	}
	cut := cli.Truncate(b.String(), w)
	if !t.styled {
		return cut
	}
	return paint(cut, segs)
}

// bar is line padded to exactly w cells with base, which also lies under
// every segment's own colours: the header row, the selection and the stop
// prompt span the full width. Unstyled, nothing is padded.
func (t theme) bar(w int, base lipgloss.Style, segs ...seg) string {
	if !t.styled {
		return t.line(w, segs...)
	}
	used := 0
	for i := range segs {
		used += lipgloss.Width(cli.EscapeControls(segs[i].text))
		segs[i].style = segs[i].style.Inherit(base)
	}
	segs = append(segs, seg{text: strings.Repeat(" ", max(w-used, 0)), style: base})
	return t.line(w, segs...)
}

// paint styles cut, a possibly truncated join of segs, cell by cell: each
// rune takes the style of the segment whose cells it occupies, and runes
// past the last segment stay plain.
func paint(cut string, segs []seg) string {
	var out, run strings.Builder
	i, left := 0, 0
	if len(segs) > 0 {
		left = lipgloss.Width(segs[0].text)
	}
	flush := func() {
		if run.Len() == 0 {
			return
		}
		if i < len(segs) {
			out.WriteString(segs[i].style.Render(run.String()))
		} else {
			out.WriteString(run.String())
		}
		run.Reset()
	}
	for _, r := range cut {
		for i < len(segs) && left <= 0 {
			flush()
			i++
			if i < len(segs) {
				left = lipgloss.Width(segs[i].text)
			}
		}
		run.WriteRune(r)
		left -= lipgloss.Width(string(r))
	}
	flush()
	return out.String()
}

// render styles s when the theme is styled.
func (t theme) render(style lipgloss.Style, s string) string {
	if !t.styled || s == "" {
		return s
	}
	return style.Render(s)
}
