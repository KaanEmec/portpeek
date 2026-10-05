package tui

import (
	"context"
	"errors"
	"fmt"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/kaanemec/portpeek/internal/cli"
	"github.com/kaanemec/portpeek/internal/inspect"
)

// stopFunc is the shared stop core: recheck identity, SIGTERM, wait.
type stopFunc func(ctx context.Context, q inspect.Query, target inspect.Process) cli.StopResult

// config holds the model's collaborators. Tests replace every field.
type config struct {
	lister   inspect.Lister
	ins      inspect.Inspector
	stop     stopFunc
	interval time.Duration
	// now is the clock used for refresh and inspection times when a
	// snapshot carries none.
	now func() time.Time
	// after returns a command that delivers msg once d has passed.
	after func(d time.Duration, msg tea.Msg) tea.Cmd
}

// screen is the view the model shows.
type screen int

const (
	screenTable screen = iota
	screenDetails
)

// Messages the model's commands produce.
type (
	listMsg struct {
		snap inspect.Snapshot
		err  error
	}
	// tickMsg asks for an automatic refresh. gen discards ticks scheduled
	// before the last pause or resume.
	tickMsg struct{ gen int }
	// detailMsg answers the inspection started for details number seq.
	detailMsg struct {
		seq int
		res inspect.Result
		err error
	}
	stopMsg struct{ res cli.StopResult }
)

// model is the Bubble Tea model of the port overview.
type model struct {
	// ctx bounds every List, Inspect and stop the model starts. Bubble Tea
	// commands take no context, so the model carries the program's.
	ctx  context.Context
	cfg  config
	keys keyMap

	width, height int

	// Table state. all is the last good snapshot in table order; visible is
	// all after the search filter.
	all     []row
	visible []row
	owners  []inspect.Owner
	cursor  int
	offset  int
	sort    sortMode
	search  textinput.Model
	// searching means the search input has focus.
	searching bool

	loaded       bool
	loading      bool
	refreshAgain bool
	refreshed    time.Time
	paused       bool
	tickGen      int
	// listErr is the friendly text of the last failed List, cleared by the
	// next successful one.
	listErr string
	// notice reports the outcome of the last stop action.
	notice      string
	noticeIsErr bool

	screen screen
	detail detailState
}

// detailState is the details view of one selected socket.
type detailState struct {
	seq       int
	query     inspect.Query
	loading   bool
	owners    []inspect.Owner
	errText   string
	inspected time.Time
	// confirm means the stop prompt is waiting for y/N for target.
	confirm  bool
	target   inspect.Process
	stopping bool
	// status is the line above the details footer: the prompt, or why
	// nothing will be stopped.
	status string
}

// newModel returns a model that starts loading on Init.
func newModel(ctx context.Context, cfg config) model {
	search := textinput.New()
	search.Prompt = "/ "
	search.Placeholder = "port or process"
	return model{
		ctx:     ctx,
		cfg:     cfg,
		keys:    defaultKeyMap(),
		search:  search,
		loading: true,
	}
}

// Init starts the first List and the auto-refresh timer.
func (m model) Init() tea.Cmd {
	return tea.Batch(m.listCmd(), m.scheduleTick())
}

// Update handles one message and keeps the selected row on screen.
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.update(msg)
	next.offset = next.clampedOffset()
	return next, cmd
}

func (m model) update(msg tea.Msg) (model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.search.SetWidth(max(m.viewWidth()-len(m.search.Prompt)-1, 1))
		return m, nil
	case listMsg:
		return m.applyList(msg)
	case tickMsg:
		return m.onTick(msg)
	case detailMsg:
		return m.applyDetail(msg), nil
	case stopMsg:
		return m.applyStop(msg)
	case tea.KeyPressMsg:
		return m.onKey(msg)
	}

	// Anything else, such as the search cursor's blink, belongs to the
	// search input.
	if m.searching {
		var cmd tea.Cmd
		m.search, cmd = m.search.Update(msg)
		return m, cmd
	}
	return m, nil
}

// listCmd lists every local port.
func (m model) listCmd() tea.Cmd {
	ctx, lister := m.ctx, m.cfg.lister
	return func() tea.Msg {
		snap, err := lister.List(ctx)
		return listMsg{snap: snap, err: err}
	}
}

// scheduleTick schedules the next automatic refresh.
func (m model) scheduleTick() tea.Cmd {
	return m.cfg.after(m.cfg.interval, tickMsg{gen: m.tickGen})
}

// refresh starts a List unless one is in flight, in which case another runs
// as soon as it returns, so the result reflects everything before the call.
func (m model) refresh() (model, tea.Cmd) {
	if m.loading {
		m.refreshAgain = true
		return m, nil
	}
	m.loading = true
	return m, m.listCmd()
}

// applyList shows a new snapshot, or keeps the last good table and reports
// the error.
func (m model) applyList(msg listMsg) (model, tea.Cmd) {
	m.loading = false
	var cmd tea.Cmd
	if m.refreshAgain {
		m.refreshAgain = false
		m, cmd = m.refresh()
	}

	if errors.Is(msg.err, context.Canceled) {
		return m, cmd
	}
	if msg.err != nil {
		m.listErr = cli.ErrorText(inspect.Query{}, msg.err)
		return m, cmd
	}

	m.listErr = ""
	m.loaded = true
	m.owners = msg.snap.Owners
	m.refreshed = msg.snap.Taken
	if m.refreshed.IsZero() {
		m.refreshed = m.cfg.now()
	}
	m.all = buildRows(msg.snap)
	sortRows(m.all, m.sort)
	m.refilter()
	return m, cmd
}

// onTick refreshes and schedules the next tick, unless the tick is stale or
// auto-refresh is paused.
func (m model) onTick(msg tickMsg) (model, tea.Cmd) {
	if msg.gen != m.tickGen || m.paused {
		return m, nil
	}
	next := m.scheduleTick()
	if m.loading {
		return m, next
	}
	m, cmd := m.refresh()
	return m, tea.Batch(cmd, next)
}

// refilter recomputes the visible rows and keeps the selection on the same
// socket. When that socket is gone, the cursor stays at the same index,
// clamped to the new rows.
func (m *model) refilter() {
	selected, hadSelection := m.selected()
	m.visible = filterRows(m.all, m.search.Value())
	if hadSelection {
		for i, r := range m.visible {
			if r.key() == selected.key() {
				m.cursor = i
				return
			}
		}
	}
	m.cursor = clamp(m.cursor, 0, len(m.visible)-1)
}

// selected returns the row under the cursor.
func (m model) selected() (row, bool) {
	if m.cursor < 0 || m.cursor >= len(m.visible) {
		return row{}, false
	}
	return m.visible[m.cursor], true
}

func (m model) onKey(msg tea.KeyPressMsg) (model, tea.Cmd) {
	if key.Matches(msg, m.keys.interrupt) {
		return m, tea.Interrupt
	}
	switch {
	case m.screen == screenDetails:
		return m.detailKey(msg)
	case m.searching:
		return m.searchKey(msg)
	default:
		return m.tableKey(msg)
	}
}

func (m model) tableKey(msg tea.KeyPressMsg) (model, tea.Cmd) {
	k := m.keys
	switch {
	case key.Matches(msg, k.quit):
		return m, tea.Quit
	case key.Matches(msg, k.clear):
		m.search.Reset()
		m.refilter()
	case key.Matches(msg, k.details):
		return m.openDetails()
	case key.Matches(msg, k.search):
		m.searching = true
		return m, m.search.Focus()
	case key.Matches(msg, k.sort):
		m.sort = m.sort.next()
		sortRows(m.all, m.sort)
		m.refilter()
	case key.Matches(msg, k.refresh):
		return m.refresh()
	case key.Matches(msg, k.pause):
		return m.togglePause()
	default:
		m.move(msg)
	}
	return m, nil
}

// move handles the navigation keys; others are ignored.
func (m *model) move(msg tea.KeyPressMsg) {
	k := m.keys
	page := max(m.bodyHeight(), 1)
	switch {
	case key.Matches(msg, k.up):
		m.cursor--
	case key.Matches(msg, k.down):
		m.cursor++
	case key.Matches(msg, k.pageUp):
		m.cursor -= page
	case key.Matches(msg, k.pageDown):
		m.cursor += page
	case key.Matches(msg, k.top):
		m.cursor = 0
	case key.Matches(msg, k.bottom):
		m.cursor = len(m.visible) - 1
	}
	m.cursor = clamp(m.cursor, 0, len(m.visible)-1)
}

// togglePause stops or resumes auto-refresh. Resuming refreshes at once.
func (m model) togglePause() (model, tea.Cmd) {
	m.paused = !m.paused
	m.tickGen++
	if m.paused {
		return m, nil
	}
	next := m.scheduleTick()
	m, cmd := m.refresh()
	return m, tea.Batch(cmd, next)
}

func (m model) searchKey(msg tea.KeyPressMsg) (model, tea.Cmd) {
	k := m.keys
	switch {
	case key.Matches(msg, k.clear):
		m.search.Reset()
		m.search.Blur()
		m.searching = false
		m.refilter()
		return m, nil
	case key.Matches(msg, k.details):
		m.search.Blur()
		m.searching = false
		return m, nil
	case key.Matches(msg, k.up, k.down, k.pageUp, k.pageDown):
		m.move(msg)
		return m, nil
	}

	var cmd tea.Cmd
	m.search, cmd = m.search.Update(msg)
	m.refilter()
	return m, cmd
}

// openDetails inspects the selected socket's port and protocol.
func (m model) openDetails() (model, tea.Cmd) {
	r, ok := m.selected()
	if !ok {
		return m, nil
	}
	q := inspect.Query{Port: r.port, Protocol: r.proto}
	seq := m.detail.seq + 1
	m.detail = detailState{seq: seq, query: q, loading: true}
	m.screen = screenDetails
	m.notice = ""

	ctx, ins := m.ctx, m.cfg.ins
	return m, func() tea.Msg {
		res, err := ins.Inspect(ctx, q)
		return detailMsg{seq: seq, res: res, err: err}
	}
}

// applyDetail shows an inspection result unless the user has moved on.
func (m model) applyDetail(msg detailMsg) model {
	stale := msg.seq != m.detail.seq || m.screen != screenDetails
	if stale || errors.Is(msg.err, context.Canceled) {
		return m
	}
	m.detail.loading = false
	m.detail.inspected = m.cfg.now()
	if msg.err != nil {
		m.detail.errText = cli.ErrorText(m.detail.query, msg.err)
		return m
	}
	m.detail.owners = msg.res.Owners
	return m
}

func (m model) detailKey(msg tea.KeyPressMsg) (model, tea.Cmd) {
	d := &m.detail
	switch {
	case d.stopping:
		return m, nil
	case d.confirm:
		d.confirm = false
		if !key.Matches(msg, m.keys.yes) {
			d.status = "Not stopped."
			return m, nil
		}
		d.stopping = true
		d.status = "Sending SIGTERM…"
		ctx, stop, q, target := m.ctx, m.cfg.stop, d.query, d.target
		return m, func() tea.Msg { return stopMsg{res: stop(ctx, q, target)} }
	case key.Matches(msg, m.keys.back):
		m.screen = screenTable
		return m, nil
	case key.Matches(msg, m.keys.stop):
		m.requestStop()
	}
	return m, nil
}

// requestStop asks for confirmation when exactly one known process owns the
// inspected port, and otherwise explains why nothing can be stopped from
// here. It never signals anything itself.
func (m *model) requestStop() {
	d := &m.detail
	port := d.query.Port
	switch {
	case d.loading:
		return
	case d.errText != "":
		d.status = "nothing to stop: the port could not be inspected"
	case len(d.owners) == 0:
		d.status = fmt.Sprintf("nothing to stop: no process uses port %d any more", port)
	case len(d.owners) > 1:
		d.status = fmt.Sprintf("several owners; use: portpeek %d --stop --pid <pid>", port)
	case d.owners[0].Process.PID <= 0:
		d.status = fmt.Sprintf("owner of port %d is unknown; it cannot be stopped", port)
	default:
		d.target = d.owners[0].Process
		d.confirm = true
		d.status = fmt.Sprintf("Send SIGTERM to %s? y/N", processLabel(d.target))
	}
}

// applyStop returns to the table with the stop result and refreshes it.
func (m model) applyStop(msg stopMsg) (model, tea.Cmd) {
	m.screen = screenTable
	m.detail = detailState{seq: m.detail.seq}
	if msg.res.Interrupted {
		return m, nil
	}
	m.notice = msg.res.Message
	m.noticeIsErr = !msg.res.Sent
	return m.refresh()
}

// processLabel names a process the way the CLI's stop prompt does.
func processLabel(p inspect.Process) string {
	if p.Name == "" {
		return fmt.Sprintf("PID %d", p.PID)
	}
	return fmt.Sprintf("%s (PID %d)", p.Name, p.PID)
}

func clamp(v, low, high int) int {
	return max(low, min(v, high))
}
