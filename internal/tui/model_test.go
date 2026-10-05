package tui

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/kaanemec/portpeek/internal/cli"
	"github.com/kaanemec/portpeek/internal/inspect"
)

var taken = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

// listResult is one answer of fakeLister.
type listResult struct {
	snap inspect.Snapshot
	err  error
}

// fakeLister returns results[n] on call n, repeating the last one.
type fakeLister struct {
	results []listResult
	calls   int
}

func (f *fakeLister) List(context.Context) (inspect.Snapshot, error) {
	r := f.results[min(f.calls, len(f.results)-1)]
	f.calls++
	return r.snap, r.err
}

// fakeInspector returns a fixed answer and records queries.
type fakeInspector struct {
	res     inspect.Result
	err     error
	queries []inspect.Query
}

func (f *fakeInspector) Inspect(_ context.Context, q inspect.Query) (inspect.Result, error) {
	f.queries = append(f.queries, q)
	res := f.res
	res.Query = q
	return res, f.err
}

// stopCall is one call of the stop core.
type stopCall struct {
	query  inspect.Query
	target inspect.Process
}

// fakeStop records calls to the stop core instead of signalling.
type fakeStop struct {
	res   cli.StopResult
	calls []stopCall
}

func (f *fakeStop) stop(_ context.Context, q inspect.Query, target inspect.Process) cli.StopResult {
	f.calls = append(f.calls, stopCall{query: q, target: target})
	return f.res
}

func process(pid int, name string) inspect.Process {
	return inspect.Process{PID: pid, Name: name, Unavailable: map[inspect.Field]string{}}
}

func sock(proto inspect.Protocol, family inspect.Family, addr string, port int) inspect.Socket {
	state := inspect.StateListen
	if proto == inspect.UDP {
		state = inspect.StateBound
	}
	return inspect.Socket{Protocol: proto, Family: family, Address: addr, Port: port, State: state}
}

func nodeOwner() inspect.Owner {
	return inspect.Owner{
		Process: process(48213, "node"),
		Sockets: []inspect.Socket{sock(inspect.TCP, inspect.IPv4, "127.0.0.1", 3000)},
	}
}

func unknownOwner() inspect.Owner {
	p := process(0, "")
	p.MarkUnavailable(inspect.FieldName, "not readable without elevated privileges")
	return inspect.Owner{
		Process: p,
		Sockets: []inspect.Socket{sock(inspect.UDP, inspect.IPv4, "0.0.0.0", 5353)},
	}
}

// baseSnapshot sorts by port as: node 3000, unknown 5353, postgres 5432,
// python 8000 (IPv4 "*"), python 8000 (IPv6 "::").
func baseSnapshot() inspect.Snapshot {
	return inspect.Snapshot{
		Taken: taken,
		Owners: []inspect.Owner{
			{
				Process: process(500, "Python3"),
				Sockets: []inspect.Socket{
					sock(inspect.TCP, inspect.IPv4, "*", 8000),
					sock(inspect.TCP, inspect.IPv6, "::", 8000),
				},
			},
			nodeOwner(),
			{
				Process: process(700, "postgres"),
				Sockets: []inspect.Socket{sock(inspect.TCP, inspect.IPv6, "::1", 5432)},
			},
			unknownOwner(),
		},
	}
}

// fixture bundles a model with its fakes.
type fixture struct {
	m      model
	lister *fakeLister
	ins    *fakeInspector
	stop   *fakeStop
}

// newFixture returns a model sized width x 30 whose first List has been
// applied. Ticks are delivered by running their command, never by waiting.
func newFixture(t *testing.T, width int, results ...listResult) *fixture {
	t.Helper()
	return newThemedFixture(t, width, false, results...)
}

// newThemedFixture is newFixture with styling on or off.
func newThemedFixture(t *testing.T, width int, styled bool, results ...listResult) *fixture {
	t.Helper()
	if len(results) == 0 {
		results = []listResult{{snap: baseSnapshot()}}
	}
	f := &fixture{
		lister: &fakeLister{results: results},
		ins:    &fakeInspector{},
		stop:   &fakeStop{},
	}
	f.m = newModel(t.Context(), config{
		lister:   f.lister,
		ins:      f.ins,
		stop:     f.stop.stop,
		interval: 5 * time.Second,
		styled:   styled,
		now:      func() time.Time { return taken.Add(time.Minute) },
		after:    func(_ time.Duration, msg tea.Msg) tea.Cmd { return func() tea.Msg { return msg } },
	})
	f.send(t, tea.WindowSizeMsg{Width: width, Height: 30})
	for _, msg := range run(t, f.m.Init()) {
		if _, ok := msg.(listMsg); ok {
			f.send(t, msg)
		}
	}
	return f
}

// send delivers msg and returns the command Update produced.
func (f *fixture) send(t *testing.T, msg tea.Msg) tea.Cmd {
	t.Helper()
	next, cmd := f.m.Update(msg)
	m, ok := next.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", next)
	}
	f.m = m
	return cmd
}

// press sends key presses in order and returns the last command.
func (f *fixture) press(t *testing.T, keys ...string) tea.Cmd {
	t.Helper()
	var cmd tea.Cmd
	for _, k := range keys {
		cmd = f.send(t, keyPress(k))
	}
	return cmd
}

// sendAll runs cmd and delivers every message it produces.
func (f *fixture) sendAll(t *testing.T, cmd tea.Cmd) {
	t.Helper()
	for _, msg := range run(t, cmd) {
		f.send(t, msg)
	}
}

// screen returns the rendered view without ANSI styling.
func (f *fixture) screen() string {
	return stripANSI(f.m.View().Content)
}

// run executes cmd, expanding one level of batching, and returns the
// messages. It never runs commands nested deeper, so a tick cannot loop.
func run(t *testing.T, cmd tea.Cmd) []tea.Msg {
	t.Helper()
	if cmd == nil {
		return nil
	}
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		return []tea.Msg{msg}
	}
	msgs := []tea.Msg{}
	for _, c := range batch {
		if c != nil {
			msgs = append(msgs, c())
		}
	}
	return msgs
}

func keyPress(k string) tea.KeyPressMsg {
	switch k {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "end":
		return tea.KeyPressMsg{Code: tea.KeyEnd}
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	default:
		return tea.KeyPressMsg{Code: []rune(k)[0], Text: k}
	}
}

var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;:?]*[ -/]*[@-~]`)

func stripANSI(s string) string {
	return ansiEscape.ReplaceAllString(s, "")
}

// visibleKeys lists the visible rows as "port/proto address process".
func visibleKeys(m model) []string {
	keys := make([]string, 0, len(m.visible))
	for _, r := range m.visible {
		keys = append(keys, r.bind.String()+"/"+string(r.bind.Protocol)+" "+r.process)
	}
	return keys
}

func selectedBinding(t *testing.T, m model) string {
	t.Helper()
	r, ok := m.selected()
	if !ok {
		t.Fatal("no row selected")
	}
	return r.bind.String()
}

func TestModel_Loading(t *testing.T) {
	t.Parallel()

	m := newModel(t.Context(), config{lister: &fakeLister{}})

	got := stripANSI(m.View().Content)
	for _, want := range []string{"Loading local ports…", "refreshing…"} {
		if !strings.Contains(got, want) {
			t.Errorf("view lacks %q:\n%s", want, got)
		}
	}
}

func TestModel_InitialLoad(t *testing.T) {
	t.Parallel()

	f := newFixture(t, 100)

	want := []string{
		"127.0.0.1:3000/tcp node",
		"0.0.0.0:5353/udp unknown",
		"[::1]:5432/tcp postgres",
		"*:8000/tcp Python3",
		"[::]:8000/tcp Python3",
	}
	if got := visibleKeys(f.m); !slices.Equal(got, want) {
		t.Errorf("rows = %q, want %q", got, want)
	}

	screen := f.screen()
	for _, line := range []string{
		"portpeek  5/5 sockets  sort: port  refreshed 12:00:00",
		"  Port   Proto  Binding         Process",
		"> 3000   tcp    127.0.0.1:3000  node",
		"  5353   udp    0.0.0.0:5353    unknown                                      -        all interfaces",
		"  5432   tcp    [::1]:5432      postgres                                     700      loopback only",
		// The CLI's hint, because an owner is unknown.
		"some owners unreadable; run with sudo",
		"↑/↓: move  enter: details  /: search  s: sort  r: refresh  p: pause  q: quit",
	} {
		if !strings.Contains(screen, line) {
			t.Errorf("view lacks %q:\n%s", line, screen)
		}
	}
	if strings.Contains(screen, "refreshing…") {
		t.Errorf("view still says refreshing:\n%s", screen)
	}
}

func TestModel_CompletenessHintMatchesCLI(t *testing.T) {
	t.Parallel()

	snap := inspect.Snapshot{Taken: taken, Owners: []inspect.Owner{nodeOwner()}}
	f := newFixture(t, 120, listResult{snap: snap})

	hint := cli.CompletenessHint(snap.Owners)
	screen := f.screen()
	if hint != "" && !strings.Contains(screen, hint) {
		t.Errorf("view lacks the CLI hint %q:\n%s", hint, screen)
	}
	if hint == "" && strings.Contains(screen, "run with sudo") {
		t.Errorf("view shows a hint for a complete answer:\n%s", screen)
	}
}

func TestModel_Search(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		query string
		want  []string
	}{
		{
			name:  "port prefix",
			query: "80",
			want:  []string{"*:8000/tcp Python3", "[::]:8000/tcp Python3"},
		},
		{
			name:  "port prefix matches several ports",
			query: "5",
			want:  []string{"0.0.0.0:5353/udp unknown", "[::1]:5432/tcp postgres"},
		},
		{
			name:  "process substring ignores case",
			query: "PYTH",
			want:  []string{"*:8000/tcp Python3", "[::]:8000/tcp Python3"},
		},
		{
			name:  "port digits inside a number do not match",
			query: "432",
			want:  []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newFixture(t, 100)
			f.press(t, "/")
			f.press(t, strings.Split(tt.query, "")...)

			if got := visibleKeys(f.m); !slices.Equal(got, tt.want) {
				t.Errorf("rows = %q, want %q", got, tt.want)
			}
			if len(tt.want) == 0 && !strings.Contains(f.screen(), `No sockets match "`+tt.query+`".`) {
				t.Errorf("view lacks the no-match line:\n%s", f.screen())
			}

			f.press(t, "esc")
			if len(f.m.visible) != 5 || f.m.searching || f.m.search.Value() != "" {
				t.Errorf("after esc: %d rows, searching %v, query %q; want 5 rows, no search",
					len(f.m.visible), f.m.searching, f.m.search.Value())
			}
		})
	}
}

func TestModel_SearchKeptAfterEnter(t *testing.T) {
	t.Parallel()

	f := newFixture(t, 100)
	f.press(t, "/", "n", "o", "d", "enter")

	if f.m.searching || len(f.m.visible) != 1 {
		t.Fatalf("searching %v with %d rows, want filter kept on 1 row", f.m.searching, len(f.m.visible))
	}
	if !strings.Contains(f.screen(), "filter: nod  (esc clears)") || !strings.Contains(f.screen(), "1/5 sockets") {
		t.Errorf("view lacks the kept filter:\n%s", f.screen())
	}

	f.press(t, "esc")
	if len(f.m.visible) != 5 {
		t.Errorf("esc in table left %d rows, want 5", len(f.m.visible))
	}
}

func TestModel_SortToggle(t *testing.T) {
	t.Parallel()

	f := newFixture(t, 100)
	f.press(t, "s")

	byProcess := []string{
		"127.0.0.1:3000/tcp node",
		"[::1]:5432/tcp postgres",
		"*:8000/tcp Python3",
		"[::]:8000/tcp Python3",
		"0.0.0.0:5353/udp unknown",
	}
	if got := visibleKeys(f.m); !slices.Equal(got, byProcess) {
		t.Errorf("rows by process = %q, want %q", got, byProcess)
	}
	if !strings.Contains(f.screen(), "sort: process") {
		t.Errorf("view lacks the sort mode:\n%s", f.screen())
	}

	f.press(t, "s")
	if got := visibleKeys(f.m); got[1] != "0.0.0.0:5353/udp unknown" {
		t.Errorf("rows after second toggle = %q, want port order", got)
	}
}

func TestModel_SortKeepsSelection(t *testing.T) {
	t.Parallel()

	f := newFixture(t, 100)
	f.press(t, "down") // 0.0.0.0:5353
	f.press(t, "s")

	if got := selectedBinding(t, f.m); got != "0.0.0.0:5353" {
		t.Errorf("selected %s after sort, want 0.0.0.0:5353", got)
	}
}

func TestModel_RefreshPreservesSelection(t *testing.T) {
	t.Parallel()

	grown := baseSnapshot()
	grown.Owners = append(grown.Owners, inspect.Owner{
		Process: process(900, "redis"),
		Sockets: []inspect.Socket{sock(inspect.TCP, inspect.IPv4, "127.0.0.1", 1000)},
	})
	f := newFixture(t, 100, listResult{snap: baseSnapshot()}, listResult{snap: grown})
	f.press(t, "down", "down") // [::1]:5432, index 2

	f.sendAll(t, f.press(t, "r"))

	if f.lister.calls != 2 {
		t.Fatalf("List calls = %d, want 2", f.lister.calls)
	}
	if got := selectedBinding(t, f.m); got != "[::1]:5432" {
		t.Errorf("selected %s after refresh, want [::1]:5432", got)
	}
	if f.m.cursor != 3 {
		t.Errorf("cursor = %d, want 3 (row moved down by the new port)", f.m.cursor)
	}
}

func TestModel_VanishedRowClamps(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		keep       func(inspect.Owner) bool
		moves      []string
		wantCursor int
		wantBind   string
	}{
		{
			name:       "last row gone clamps to the new last row",
			keep:       func(o inspect.Owner) bool { return o.Process.PID != 500 },
			moves:      []string{"end"},
			wantCursor: 2,
			wantBind:   "[::1]:5432",
		},
		{
			name:       "middle row gone keeps the index",
			keep:       func(o inspect.Owner) bool { return o.Process.PID != 700 },
			moves:      []string{"down", "down"},
			wantCursor: 2,
			wantBind:   "*:8000",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			shrunk := baseSnapshot()
			shrunk.Owners = slices.DeleteFunc(shrunk.Owners, func(o inspect.Owner) bool { return !tt.keep(o) })
			f := newFixture(t, 100, listResult{snap: baseSnapshot()}, listResult{snap: shrunk})
			f.press(t, tt.moves...)

			f.sendAll(t, f.press(t, "r"))

			if f.m.cursor != tt.wantCursor {
				t.Errorf("cursor = %d, want %d", f.m.cursor, tt.wantCursor)
			}
			if got := selectedBinding(t, f.m); got != tt.wantBind {
				t.Errorf("selected %s, want %s", got, tt.wantBind)
			}
		})
	}
}

func TestModel_ListErrorKeepsRows(t *testing.T) {
	t.Parallel()

	denied := &inspect.Error{Kind: inspect.KindPermissionDenied, Op: "lsof", Err: errors.New("operation not permitted")}
	f := newFixture(t, 120, listResult{snap: baseSnapshot()}, listResult{err: denied}, listResult{snap: baseSnapshot()})

	f.sendAll(t, f.press(t, "r"))

	if len(f.m.visible) != 5 {
		t.Errorf("rows = %d after a failed List, want the 5 previous rows", len(f.m.visible))
	}
	screen := f.screen()
	want := "permission denied while listing local ports. Re-run with sudo for full details: sudo portpeek tui"
	if !strings.Contains(screen, want) {
		t.Errorf("view lacks %q:\n%s", want, screen)
	}
	if !strings.Contains(screen, "refreshed 12:00:00") {
		t.Errorf("view lost the last good refresh time:\n%s", screen)
	}

	f.sendAll(t, f.press(t, "r"))
	if strings.Contains(f.screen(), "permission denied") {
		t.Errorf("error still shown after a successful List:\n%s", f.screen())
	}
}

func TestModel_FirstListError(t *testing.T) {
	t.Parallel()

	missing := &inspect.Error{Kind: inspect.KindToolMissing, Op: "lsof", Err: errors.New("not found")}
	f := newFixture(t, 120, listResult{err: missing})

	screen := f.screen()
	for _, want := range []string{
		"No data yet.",
		`required tool "lsof" is not installed, so local ports cannot be listed.`,
	} {
		if !strings.Contains(screen, want) {
			t.Errorf("view lacks %q:\n%s", want, screen)
		}
	}
}

func TestModel_RefreshWhileLoadingRunsAgain(t *testing.T) {
	t.Parallel()

	f := newFixture(t, 100)
	first := f.press(t, "r")
	if cmd := f.press(t, "r"); cmd != nil {
		t.Fatal("second refresh started a List while one was in flight")
	}

	again := f.send(t, run(t, first)[0])

	if again == nil {
		t.Fatal("no follow-up List after a refresh requested mid-flight")
	}
	if !f.m.loading || !strings.Contains(f.screen(), "refreshing…") {
		t.Errorf("follow-up List not shown as in flight:\n%s", f.screen())
	}
}

func TestModel_AutoRefreshAndPause(t *testing.T) {
	t.Parallel()

	f := newFixture(t, 100)

	msgs := run(t, f.send(t, tickMsg{gen: 0}))
	if len(msgs) != 2 {
		t.Fatalf("tick produced %d messages, want a List and the next tick", len(msgs))
	}
	if f.lister.calls != 2 {
		t.Errorf("List calls = %d after a tick, want 2", f.lister.calls)
	}

	f.press(t, "p")
	if !strings.Contains(f.screen(), "paused") {
		t.Errorf("view lacks paused:\n%s", f.screen())
	}
	f.send(t, listMsg{snap: baseSnapshot()})
	if cmd := f.send(t, tickMsg{gen: 0}); cmd != nil {
		t.Error("a tick scheduled before pausing still refreshed")
	}

	resumed := run(t, f.press(t, "p"))
	if len(resumed) != 2 {
		t.Fatalf("resume produced %d messages, want a List and a new tick", len(resumed))
	}
	if tick, ok := resumed[1].(tickMsg); !ok || tick.gen != f.m.tickGen {
		t.Errorf("resume scheduled %#v, want a tick of the current generation %d", resumed[1], f.m.tickGen)
	}
}

func TestModel_Quit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		keys []string
		want tea.Msg
	}{
		{name: "q quits", keys: []string{"q"}, want: tea.QuitMsg{}},
		{name: "ctrl+c interrupts", keys: []string{"ctrl+c"}, want: tea.InterruptMsg{}},
		{name: "ctrl+c interrupts while searching", keys: []string{"/", "ctrl+c"}, want: tea.InterruptMsg{}},
		{name: "ctrl+c interrupts in details", keys: []string{"enter", "ctrl+c"}, want: tea.InterruptMsg{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newFixture(t, 100)
			cmd := f.press(t, tt.keys...)
			if cmd == nil {
				t.Fatal("no command")
			}
			if got := cmd(); got != tt.want {
				t.Errorf("command produced %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestModel_Details(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		owners   []inspect.Owner
		wantText string
	}{
		{
			name:     "owner",
			owners:   []inspect.Owner{nodeOwner()},
			wantText: cli.RenderDetail(inspect.Query{Port: 3000, Protocol: inspect.TCP}, []inspect.Owner{nodeOwner()}),
		},
		{
			name:     "port gone since the list",
			wantText: "no listening or bound socket on 3000 (tcp)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newFixture(t, 140)
			f.ins.res = inspect.Result{Owners: tt.owners}
			f.press(t, "s", "/", "n", "o", "d", "enter") // sorted by process, filtered to node

			cmd := f.press(t, "enter")
			if !strings.Contains(f.screen(), "Port 3000/tcp  inspecting…") {
				t.Errorf("view lacks the inspecting state:\n%s", f.screen())
			}
			f.sendAll(t, cmd)

			want := []inspect.Query{{Port: 3000, Protocol: inspect.TCP}}
			if !slices.Equal(f.ins.queries, want) {
				t.Errorf("queries = %+v, want %+v", f.ins.queries, want)
			}
			screen := f.screen()
			for _, line := range strings.Split(strings.TrimSuffix(tt.wantText, "\n"), "\n") {
				if !strings.Contains(screen, line) {
					t.Errorf("details lack CLI line %q:\n%s", line, screen)
				}
			}
			for _, line := range []string{"Port 3000/tcp  inspected 12:01:00", "Copy: portpeek 3000 --tcp", "k: stop  Esc: back"} {
				if !strings.Contains(screen, line) {
					t.Errorf("details lack %q:\n%s", line, screen)
				}
			}

			f.press(t, "esc")
			if f.m.screen != screenTable || f.m.sort != sortByProcess || f.m.search.Value() != "nod" {
				t.Errorf("back in table: screen %v, sort %v, query %q; want table, process sort, query nod",
					f.m.screen, f.m.sort, f.m.search.Value())
			}
			if got := selectedBinding(t, f.m); got != "127.0.0.1:3000" {
				t.Errorf("selected %s after details, want 127.0.0.1:3000", got)
			}
		})
	}
}

func TestModel_DetailsInspectError(t *testing.T) {
	t.Parallel()

	f := newFixture(t, 140)
	f.ins.err = &inspect.Error{Kind: inspect.KindPermissionDenied, Op: "lsof", Err: errors.New("denied")}
	f.sendAll(t, f.press(t, "enter"))

	want := "permission denied while inspecting port 3000. Re-run with sudo for full details: sudo portpeek 3000"
	if !strings.Contains(f.screen(), want) {
		t.Errorf("details lack %q:\n%s", want, f.screen())
	}
	f.press(t, "k")
	if len(f.stop.calls) != 0 || f.m.detail.confirm {
		t.Error("stop offered for a port that could not be inspected")
	}
}

func TestModel_StaleDetailIgnored(t *testing.T) {
	t.Parallel()

	f := newFixture(t, 100)
	f.ins.res = inspect.Result{Owners: []inspect.Owner{nodeOwner()}}
	cmd := f.press(t, "enter")
	f.press(t, "esc")

	f.sendAll(t, cmd)

	if f.m.screen != screenTable {
		t.Errorf("a late inspection reopened details")
	}
}

func TestModel_StopRefused(t *testing.T) {
	t.Parallel()

	python := inspect.Owner{
		Process: process(500, "python3"),
		Sockets: []inspect.Socket{sock(inspect.TCP, inspect.IPv4, "127.0.0.1", 3000)},
	}
	tests := []struct {
		name       string
		owners     []inspect.Owner
		wantStatus string
	}{
		{
			name:       "unknown owner",
			owners:     []inspect.Owner{unknownOwner()},
			wantStatus: "owner of port 3000 is unknown; it cannot be stopped",
		},
		{
			name:       "several owners",
			owners:     []inspect.Owner{nodeOwner(), python},
			wantStatus: "several owners; use: portpeek 3000 --stop --pid <pid>",
		},
		{
			name:       "known and unknown owner",
			owners:     []inspect.Owner{nodeOwner(), unknownOwner()},
			wantStatus: "several owners; use: portpeek 3000 --stop --pid <pid>",
		},
		{
			name:       "port gone",
			wantStatus: "nothing to stop: no process uses port 3000 any more",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newFixture(t, 140)
			f.ins.res = inspect.Result{Owners: tt.owners}
			f.sendAll(t, f.press(t, "enter"))

			f.press(t, "k")
			if cmd := f.press(t, "y"); cmd != nil {
				t.Error("y after a refusal produced a command")
			}

			if len(f.stop.calls) != 0 {
				t.Errorf("stop core called %d times, want 0", len(f.stop.calls))
			}
			if !strings.Contains(f.screen(), tt.wantStatus) {
				t.Errorf("details lack %q:\n%s", tt.wantStatus, f.screen())
			}
		})
	}
}

func TestModel_StopInTableDoesNothing(t *testing.T) {
	t.Parallel()

	f := newFixture(t, 100)
	f.press(t, "k", "y")

	if len(f.stop.calls) != 0 || f.m.screen != screenTable {
		t.Errorf("k in the table: %d stop calls, screen %v; want none and table", len(f.stop.calls), f.m.screen)
	}
}

func TestModel_StopConfirmed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		res        cli.StopResult
		wantNotice string
	}{
		{
			name:       "process exited",
			res:        cli.StopResult{Sent: true, Exited: true, Message: "Sent SIGTERM to node (PID 48213); process exited."},
			wantNotice: "Sent SIGTERM to node (PID 48213); process exited.",
		},
		{
			name:       "identity changed",
			res:        cli.StopResult{Message: "process changed since inspection; nothing was stopped"},
			wantNotice: "process changed since inspection; nothing was stopped",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newFixture(t, 140)
			f.ins.res = inspect.Result{Owners: []inspect.Owner{nodeOwner()}}
			f.stop.res = tt.res
			f.sendAll(t, f.press(t, "enter"))

			f.press(t, "k")
			if !strings.Contains(f.screen(), "Send SIGTERM to node (PID 48213)? y/N") {
				t.Fatalf("details lack the prompt:\n%s", f.screen())
			}
			if len(f.stop.calls) != 0 {
				t.Fatal("stop core called before confirmation")
			}

			stopped := run(t, f.press(t, "y"))
			want := []stopCall{{query: inspect.Query{Port: 3000, Protocol: inspect.TCP}, target: nodeOwner().Process}}
			if len(f.stop.calls) != 1 || f.stop.calls[0].query != want[0].query ||
				f.stop.calls[0].target.PID != 48213 || f.stop.calls[0].target.Name != "node" {
				t.Fatalf("stop calls = %+v, want %+v", f.stop.calls, want)
			}

			listed := f.lister.calls
			f.sendAll(t, f.send(t, stopped[0]))

			if f.m.screen != screenTable {
				t.Errorf("screen = %v after stop, want table", f.m.screen)
			}
			if f.lister.calls != listed+1 {
				t.Errorf("List calls = %d after stop, want %d (refresh)", f.lister.calls, listed+1)
			}
			if !strings.Contains(f.screen(), tt.wantNotice) {
				t.Errorf("table lacks %q:\n%s", tt.wantNotice, f.screen())
			}
		})
	}
}

func TestModel_StopDeclined(t *testing.T) {
	t.Parallel()

	f := newFixture(t, 140)
	f.ins.res = inspect.Result{Owners: []inspect.Owner{nodeOwner()}}
	f.sendAll(t, f.press(t, "enter"))

	f.press(t, "k", "n")

	if len(f.stop.calls) != 0 {
		t.Errorf("stop core called %d times, want 0", len(f.stop.calls))
	}
	if !strings.Contains(f.screen(), "Not stopped.") || f.m.screen != screenDetails {
		t.Errorf("want details with Not stopped.:\n%s", f.screen())
	}
}

func TestModel_Width(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		width       int
		wantColumns []string
		hidden      []string
	}{
		{name: "wide", width: 100, wantColumns: []string{"Binding", "Process", "PID", "Exposure"}},
		{name: "below 80 hides exposure", width: 79, wantColumns: []string{"Binding", "PID"}, hidden: []string{"Exposure"}},
		{
			name:        "below 60 hides binding",
			width:       59,
			wantColumns: []string{"Port", "Proto", "Process", "PID"},
			hidden:      []string{"Binding", "Exposure"},
		},
		{name: "20 columns", width: 20, hidden: []string{"Binding", "Exposure"}},
		{name: "1 column", width: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newFixture(t, tt.width)
			screens := []string{f.screen()}
			f.press(t, "/", "x")
			screens = append(screens, f.screen())
			f.press(t, "esc")
			f.ins.res = inspect.Result{Owners: []inspect.Owner{nodeOwner()}}
			f.sendAll(t, f.press(t, "enter"))
			f.press(t, "k")
			screens = append(screens, f.screen())

			header := strings.Split(screens[0], "\n")[1]
			for _, col := range tt.wantColumns {
				if !strings.Contains(header, col) {
					t.Errorf("header %q lacks %s", header, col)
				}
			}
			for _, col := range tt.hidden {
				if strings.Contains(header, col) {
					t.Errorf("header %q shows %s", header, col)
				}
			}
			for _, screen := range screens {
				for _, line := range strings.Split(screen, "\n") {
					if w := lipgloss.Width(line); w > max(tt.width, 1) {
						t.Errorf("line is %d cells wide, over %d: %q", w, tt.width, line)
					}
				}
			}
		})
	}
}

func TestModel_ProcessTruncated(t *testing.T) {
	t.Parallel()

	long := inspect.Snapshot{Taken: taken, Owners: []inspect.Owner{{
		Process: process(42, strings.Repeat("very-long-process-name-", 5)),
		Sockets: []inspect.Socket{sock(inspect.TCP, inspect.IPv4, "127.0.0.1", 9000)},
	}}}
	f := newFixture(t, 70, listResult{snap: long})

	row := strings.Split(f.screen(), "\n")[2]
	if !strings.Contains(row, "very-long-process-name-very-…") || !strings.Contains(row, "42") {
		t.Errorf("row %q does not truncate the name and keep the PID", row)
	}
}

func TestModel_Scrolls(t *testing.T) {
	t.Parallel()

	snap := inspect.Snapshot{Taken: taken}
	for port := 1000; port < 1100; port++ {
		snap.Owners = append(snap.Owners, inspect.Owner{
			Process: process(port, "svc"),
			Sockets: []inspect.Socket{sock(inspect.TCP, inspect.IPv4, "127.0.0.1", port)},
		})
	}
	f := newFixture(t, 100, listResult{snap: snap})

	f.press(t, "end")

	if !strings.Contains(f.screen(), "> 1099") {
		t.Errorf("last row not on screen after end:\n%s", f.screen())
	}
	if lines := strings.Count(f.screen(), "\n") + 1; lines > 30 {
		t.Errorf("view has %d lines, over the 30-line terminal", lines)
	}
}
