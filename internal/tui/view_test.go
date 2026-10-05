package tui

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/kaanemec/portpeek/internal/inspect"
)

// layoutSnapshot has names of every awkward kind (spaces, wide runes, a tab,
// an escape sequence, no name) followed by enough plain rows to scroll.
func layoutSnapshot() inspect.Snapshot {
	snap := inspect.Snapshot{Taken: taken, Owners: []inspect.Owner{unknownOwner()}}
	names := []string{"limactl", "SetappAgent", "Codex (Service)", "名前サービス", "tab\there", "esc\x1b[2Jape"}
	for i, name := range names {
		snap.Owners = append(snap.Owners, inspect.Owner{
			Process: process(43124+i, name),
			Sockets: []inspect.Socket{sock(inspect.TCP, inspect.IPv4, "*", 53+i)},
		})
	}
	for port := 2000; port < 2040; port++ {
		snap.Owners = append(snap.Owners, inspect.Owner{
			Process: process(port, "svc"),
			Sockets: []inspect.Socket{sock(inspect.TCP, inspect.IPv4, "127.0.0.1", port)},
		})
	}
	return snap
}

// newSizedFixture returns a fixture over layoutSnapshot resized to w x h.
func newSizedFixture(t *testing.T, w, h int) *fixture {
	t.Helper()
	f := newFixture(t, w, listResult{snap: layoutSnapshot()})
	f.send(t, tea.WindowSizeMsg{Width: w, Height: h})
	return f
}

// cellsOf splits plain text into display cells: a wide rune takes its cell
// and an empty continuation cell, as on a terminal.
func cellsOf(line string) []string {
	cells := []string{}
	for _, r := range line {
		cells = append(cells, string(r))
		for range lipgloss.Width(string(r)) - 1 {
			cells = append(cells, "")
		}
	}
	return cells
}

// drawnLines composes content on a w x h cell grid the way Bubble Tea's
// renderer does and returns each row's cells.
func drawnLines(content string, w, h int) [][]string {
	canvas := lipgloss.NewCanvas(w, h).Compose(lipgloss.NewLayer(content))
	lines := make([][]string, 0, h)
	for y := range h {
		row := make([]string, 0, w)
		for x := range w {
			cell := canvas.CellAt(x, y)
			if cell == nil {
				row = append(row, " ")
				continue
			}
			row = append(row, cell.Content)
		}
		lines = append(lines, row)
	}
	return lines
}

// strippedLines is the view without ANSI styling, one cell slice per line.
func strippedLines(content string) [][]string {
	lines := [][]string{}
	for _, line := range strings.Split(stripANSI(content), "\n") {
		lines = append(lines, cellsOf(line))
	}
	return lines
}

// span joins cells [from, to) of line, padding past its end with spaces.
func span(line []string, from, to int) string {
	var b strings.Builder
	for x := from; x < to; x++ {
		if x < len(line) {
			b.WriteString(line[x])
			continue
		}
		b.WriteString(" ")
	}
	return b.String()
}

// checkTableLine reports a column of line whose text is not exactly
// want[i], padded, at that column's offset, or a gap that is not blank.
func checkTableLine(t *testing.T, label string, line []string, marker string, cols []column, want []string) {
	t.Helper()
	if got := span(line, 0, markerWidth); got != pad(marker, markerWidth) {
		t.Errorf("%s: marker %q, want %q", label, got, pad(marker, markerWidth))
	}
	x := markerWidth
	for i, c := range cols {
		if got, cell := span(line, x, x+c.width), pad(want[i], c.width); got != cell {
			t.Errorf("%s: %s column at cells %d-%d is %q, want %q", label, c.title, x, x+c.width, got, cell)
		}
		x += c.width
		if i < len(cols)-1 {
			if gap := span(line, x, x+len(columnGap)); gap != columnGap {
				t.Errorf("%s: gap after %s at cell %d is %q, want blank", label, c.title, x, gap)
			}
			x += len(columnGap)
		}
	}
}

// checkTable checks the header and every row of the table at the offsets
// columns() gives, both in the styled view and as drawn on a cell grid.
func checkTable(t *testing.T, f *fixture) {
	t.Helper()
	m := f.m
	cols := m.columns()
	content := m.View().Content
	views := map[string][][]string{
		"stripped": strippedLines(content),
		"drawn":    drawnLines(content, m.viewWidth(), m.viewHeight()),
	}
	for name, lines := range views {
		titles := make([]string, 0, len(cols))
		for _, c := range cols {
			titles = append(titles, c.title)
		}
		checkTableLine(t, name+" header", lines[1], "", cols, titles)

		offset := m.clampedOffset()
		for i := range m.bodyHeight() {
			idx := offset + i
			if idx >= len(m.visible) {
				break
			}
			r := m.visible[idx]
			marker := ""
			if idx == m.cursor {
				marker = ">"
			}
			want := make([]string, 0, len(cols))
			for _, c := range cols {
				want = append(want, c.cell(r))
			}
			checkTableLine(t, fmt.Sprintf("%s row %d (%s)", name, idx, r.process), lines[2+i], marker, cols, want)
		}
	}
}

func TestView_ColumnsAligned(t *testing.T) {
	t.Parallel()

	for _, w := range []int{100, 80, 79, 60, 59, 40} {
		t.Run(fmt.Sprint(w), func(t *testing.T) {
			t.Parallel()

			f := newSizedFixture(t, w, 25)
			checkTable(t, f)

			// The selection moves onto rows of every kind; selected and
			// unselected rows must line up the same way.
			for range 8 {
				f.press(t, "down")
				checkTable(t, f)
			}
		})
	}
}

func TestView_ControlCharactersEscaped(t *testing.T) {
	t.Parallel()

	f := newSizedFixture(t, 100, 25)
	screen := f.screen()
	for _, want := range []string{`tab\there`, `esc\x1b[2Jape`} {
		if !strings.Contains(screen, want) {
			t.Errorf("screen lacks escaped name %q:\n%s", want, screen)
		}
	}
	if strings.ContainsAny(stripANSI(f.m.View().Content), "\t\x1b") {
		t.Error("a control character from a process name reached the view")
	}
}

var tableRow = regexp.MustCompile(`^(> |  )\d+ `)

func TestView_FitsTerminal(t *testing.T) {
	t.Parallel()

	// Two footer lines: the completeness hint and key help.
	tests := []struct {
		w, h     int
		wantRows int
	}{
		{w: 100, h: 25, wantRows: 21},
		{w: 80, h: 15, wantRows: 11},
		{w: 60, h: 12, wantRows: 8},
		{w: 40, h: 8, wantRows: 4},
		{w: 100, h: 5, wantRows: 1},
		{w: 100, h: 4, wantRows: 1},
		{w: 100, h: 3, wantRows: 1},
		{w: 100, h: 2, wantRows: 0},
		{w: 20, h: 1, wantRows: 0},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%dx%d", tt.w, tt.h), func(t *testing.T) {
			t.Parallel()

			f := newSizedFixture(t, tt.w, tt.h)
			checkFits(t, f, tt.w, tt.h, "start")
			if got := countRows(f.screen()); got != tt.wantRows {
				t.Errorf("start: %d table rows, want %d:\n%s", got, tt.wantRows, f.screen())
			}

			// Walk past the bottom of the window: the selected row stays on
			// screen and the window stays the same size.
			for i := range len(f.m.visible) + 3 {
				f.press(t, "down")
				label := fmt.Sprintf("down %d", i+1)
				checkFits(t, f, tt.w, tt.h, label)
				if got := countRows(f.screen()); got != tt.wantRows {
					t.Fatalf("%s: %d table rows, want %d:\n%s", label, got, tt.wantRows, f.screen())
				}
				if tt.wantRows > 0 && !strings.Contains(f.screen(), "\n> ") {
					t.Fatalf("%s: selected row scrolled off screen:\n%s", label, f.screen())
				}
			}

			f.press(t, "/", "2")
			checkFits(t, f, tt.w, tt.h, "searching")
			f.press(t, "esc")

			f.ins.res = inspect.Result{Owners: []inspect.Owner{nodeOwner()}}
			f.sendAll(t, f.press(t, "enter"))
			f.press(t, "k")
			checkFits(t, f, tt.w, tt.h, "details")
			f.press(t, "n", "esc")
			checkFits(t, f, tt.w, tt.h, "back")
		})
	}
}

// checkFits reports a view with more lines than the terminal has rows, a
// line wider than the terminal, or a view outside the alternate screen.
func checkFits(t *testing.T, f *fixture, w, h int, label string) {
	t.Helper()
	v := f.m.View()
	if !v.AltScreen {
		t.Errorf("%s: view is not on the alternate screen", label)
	}
	lines := strings.Split(stripANSI(v.Content), "\n")
	if len(lines) > h {
		t.Errorf("%s: view has %d lines, over the %d-row terminal", label, len(lines), h)
	}
	for _, line := range lines {
		if lw := lipgloss.Width(line); lw > w {
			t.Errorf("%s: line is %d cells wide, over %d: %q", label, lw, w, line)
		}
	}
}

func countRows(screen string) int {
	n := 0
	for _, line := range strings.Split(screen, "\n") {
		if tableRow.MatchString(line) {
			n++
		}
	}
	return n
}

func TestEscapeControls(t *testing.T) {
	t.Parallel()

	tests := []struct{ in, want string }{
		{in: "plain name", want: "plain name"},
		{in: "名前", want: "名前"},
		{in: "a\tb", want: `a\tb`},
		{in: "x\x1b[31my", want: `x\x1b[31my`},
		{in: "nl\n", want: `nl\n`},
		{in: "c1\u0085", want: `c1\u0085`},
	}
	for _, tt := range tests {
		if got := escapeControls(tt.in); got != tt.want {
			t.Errorf("escapeControls(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
