package ui

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/fsnotify/fsnotify"

	"ctabs/internal/agent"
	"ctabs/internal/app"
	"ctabs/internal/notify"
	"ctabs/internal/store"
	"ctabs/internal/tmux"
	"ctabs/internal/worktree"
)

const (
	refreshEvery = 500 * time.Millisecond
	diffEvery    = 3 * time.Second
	rowHeight    = 5 // four lines of content + one spacer
	// A busy session with no hook event for this long gets its screen checked.
	quietAfter = 2 * time.Second
	// Consecutive screen checks needed before overriding the hook state.
	idleHits     = 3
	waitingHits  = 2
	headerHeight = 2
	footerHeight = 2
)

type row struct {
	s      store.Session
	st     store.Status
	seen   time.Time
	gone   bool         // pane dead or missing
	screen agent.Screen // only inspected for quiet busy sessions
}

func (r row) unseen() bool { return !r.gone && r.st.Unseen(r.seen) }

type (
	refreshMsg struct {
		rows  []row
		stage string
		at    time.Time
	}
	diffMsg  map[string]worktree.Stats
	tickMsg  struct{}
	diffTick struct{}
	watchMsg struct{}
	errMsg   struct{ err error }
)

// screenHits counts consecutive screen checks that disagree with the hooks.
type screenHits struct {
	screen agent.Screen
	n      int
}

type Sidebar struct {
	cfg       app.Config
	rows      []row
	diffs     map[string]worktree.Stats
	cursor    int
	offset    int
	stage     string
	swappedAt time.Time
	w, h      int
	spin      spinner.Model
	help      bool
	err       string
	errAt     time.Time
	watch     chan struct{}
	hits      map[string]screenHits
}

func NewSidebar() *Sidebar {
	a, _ := agent.Get("claude")
	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot), spinner.WithStyle(sWorking))
	m := &Sidebar{cfg: app.LoadConfig(a), diffs: map[string]worktree.Stats{}, hits: map[string]screenHits{}, spin: sp, w: tmux.SidebarWidth, h: 24}
	m.watch = watchStatusDir()
	return m
}

// watchStatusDir signals (coalesced) whenever a hook writes a status file.
func watchStatusDir() chan struct{} {
	_ = os.MkdirAll(store.StatusDir(), 0o755)
	ch := make(chan struct{}, 1)
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return ch
	}
	if err := w.Add(store.StatusDir()); err != nil {
		w.Close()
		return ch
	}
	go func() {
		for {
			select {
			case _, ok := <-w.Events:
				if !ok {
					return
				}
				select {
				case ch <- struct{}{}:
				default:
				}
			case _, ok := <-w.Errors:
				if !ok {
					return
				}
			}
		}
	}()
	return ch
}

func (m *Sidebar) Init() tea.Cmd {
	return tea.Batch(m.spin.Tick, load, loadDiffs(nil), tick(), diffTicker(), m.waitWatch())
}

func tick() tea.Cmd {
	return tea.Tick(refreshEvery, func(time.Time) tea.Msg { return tickMsg{} })
}

func diffTicker() tea.Cmd {
	return tea.Tick(diffEvery, func(time.Time) tea.Msg { return diffTick{} })
}

func (m *Sidebar) waitWatch() tea.Cmd {
	return func() tea.Msg {
		<-m.watch
		return watchMsg{}
	}
}

func load() tea.Msg {
	at := time.Now()
	st, err := store.Load()
	if err != nil {
		return errMsg{err}
	}
	panes, _ := tmux.Panes()
	stage, _ := tmux.Stage()
	rows := make([]row, len(st.Sessions))
	for i, s := range st.Sessions {
		status, _ := store.LoadStatus(s.ID)
		p, ok := panes[s.PaneID]
		r := row{s: s, st: status, seen: store.LoadSeen(s.ID), gone: !ok || p.Dead}
		// Being on stage is looking at it.
		if s.PaneID == stage && r.unseen() {
			if store.MarkSeen(s.ID) == nil {
				r.seen = time.Now()
			}
		}
		if !r.gone && status.Busy() && time.Since(status.Updated) > quietAfter {
			if a, err := agent.Get(s.Agent); err == nil {
				if screen, err := tmux.Capture(s.PaneID); err == nil {
					r.screen = a.Inspect(screen)
				}
			}
		}
		rows[i] = r
	}
	return refreshMsg{rows: rows, stage: stage, at: at}
}

func loadDiffs(rows []row) tea.Cmd {
	return func() tea.Msg {
		out := diffMsg{}
		for _, r := range rows {
			if d, err := worktree.Diff(r.s.Worktree, r.s.BaseCommit); err == nil {
				out[r.s.ID] = d
			}
		}
		return out
	}
}

func (m *Sidebar) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.clampOffset()
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd
	case tickMsg:
		return m, tea.Batch(load, tick())
	case watchMsg:
		return m, tea.Batch(load, m.waitWatch())
	case diffTick:
		return m, tea.Batch(loadDiffs(m.rows), diffTicker())
	case diffMsg:
		m.diffs = msg
	case errMsg:
		m.err, m.errAt = msg.err.Error(), time.Now()
	case refreshMsg:
		hadRows := len(m.rows)
		m.rows = msg.rows
		// Follow the stage when it changed from outside (M-j, M-1, …), but ignore
		// snapshots taken before our own last swap.
		if msg.stage != m.stage && msg.at.After(m.swappedAt) {
			m.stage = msg.stage
			for i, r := range m.rows {
				if r.s.PaneID == msg.stage {
					m.cursor = i
				}
			}
		}
		m.cursor = min(m.cursor, max(len(m.rows)-1, 0))
		m.clampOffset()
		cmds := []tea.Cmd{m.reconcile()}
		if len(m.rows) != hadRows {
			cmds = append(cmds, loadDiffs(m.rows))
		}
		return m, tea.Batch(cmds...)
	case tea.MouseMsg:
		return m, m.mouse(msg)
	case tea.KeyMsg:
		return m, m.key(msg)
	}
	return m, nil
}

func (m *Sidebar) key(msg tea.KeyMsg) tea.Cmd {
	if m.help {
		m.help = false
		return nil
	}
	n := len(m.rows)
	switch k := msg.String(); k {
	case "j", "down", "tab":
		if n > 0 {
			return m.selectRow((m.cursor+1)%n, false)
		}
	case "k", "up", "shift+tab":
		if n > 0 {
			return m.selectRow((m.cursor-1+n)%n, false)
		}
	case "g", "home":
		return m.selectRow(0, false)
	case "G", "end":
		return m.selectRow(n-1, false)
	case "enter", "l", "right":
		return m.selectRow(m.cursor, true)
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		i, _ := strconv.Atoi(k)
		return m.selectRow(i-1, false)
	case "n", "alt+n":
		return popup("76", "30", "new")
	case "x", "d":
		if r, ok := m.current(); ok {
			return popup("70", "14", "close", r.s.ID)
		}
	case "r":
		if r, ok := m.current(); ok {
			return popup("56", "8", "rename", r.s.ID)
		}
	case "R":
		if r, ok := m.current(); ok {
			id := r.s.ID
			return func() tea.Msg {
				if err := app.ShowSession(id, true); err != nil {
					return errMsg{err}
				}
				return load()
			}
		}
	case ".":
		st, _ := store.Load()
		if id, ok := app.NextNeedingAttention(st, m.stage); ok {
			for i, r := range m.rows {
				if r.s.ID == id {
					return m.selectRow(i, false)
				}
			}
		}
	case "?":
		m.help = true
	case "q", "ctrl+c":
		return func() tea.Msg {
			_, _ = tmux.Run("detach-client")
			return nil
		}
	}
	return nil
}

func (m *Sidebar) mouse(msg tea.MouseMsg) tea.Cmd {
	switch msg.Button {
	case tea.MouseButtonWheelDown:
		if msg.Action == tea.MouseActionPress && m.cursor < len(m.rows)-1 {
			return m.selectRow(m.cursor+1, false)
		}
	case tea.MouseButtonWheelUp:
		if msg.Action == tea.MouseActionPress && m.cursor > 0 {
			return m.selectRow(m.cursor-1, false)
		}
	case tea.MouseButtonLeft:
		if msg.Action != tea.MouseActionPress || msg.Y < headerHeight {
			return nil
		}
		i := m.offset + (msg.Y-headerHeight)/rowHeight
		if (msg.Y-headerHeight)%rowHeight < rowHeight-1 && i < len(m.rows) {
			return m.selectRow(i, i == m.cursor) // click again to focus
		}
	}
	return nil
}

func (m *Sidebar) current() (row, bool) {
	if m.cursor < 0 || m.cursor >= len(m.rows) {
		return row{}, false
	}
	return m.rows[m.cursor], true
}

// selectRow moves the cursor and stages that session; focus also moves into it.
func (m *Sidebar) selectRow(i int, focus bool) tea.Cmd {
	if i < 0 || i >= len(m.rows) {
		return nil
	}
	m.cursor = i
	m.clampOffset()
	r := m.rows[i]
	if !r.gone {
		// Swap synchronously so the stage and cursor never disagree.
		if err := tmux.Show(r.s.PaneID, focus); err != nil {
			m.err, m.errAt = err.Error(), time.Now()
			return nil
		}
		m.stage, m.swappedAt = r.s.PaneID, time.Now()
		return nil
	}
	id := r.s.ID
	m.swappedAt = time.Now()
	return func() tea.Msg {
		if err := app.ShowSession(id, focus); err != nil {
			return errMsg{err}
		}
		return load()
	}
}

func popup(w, h string, args ...string) tea.Cmd {
	return func() tea.Msg {
		_ = tmux.Popup(w, h, append([]string{app.Exe()}, args...))
		return load()
	}
}

func (m *Sidebar) visibleRows() int {
	return max((m.h-headerHeight-footerHeight)/rowHeight, 1)
}

func (m *Sidebar) clampOffset() {
	v := m.visibleRows()
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+v {
		m.offset = m.cursor - v + 1
	}
	m.offset = max(0, min(m.offset, len(m.rows)-v))
}

// ---- view ----

func (m *Sidebar) View() string {
	w := max(m.w, 12)
	var b strings.Builder

	b.WriteString(spread(" "+sTitle.Render("ctabs"), m.summary()+" ", w) + "\n")
	b.WriteString(sFaint.Render(strings.Repeat("─", w)) + "\n")

	bodyH := m.h - headerHeight - footerHeight
	var body []string
	switch {
	case m.help:
		body = m.helpLines(w)
	case len(m.rows) == 0:
		body = []string{"", " " + sDim.Render("No sessions yet."), "", " " + sKey.Render("n") + sDim.Render(" start one in a worktree")}
	default:
		end := min(m.offset+m.visibleRows(), len(m.rows))
		for i := m.offset; i < end; i++ {
			body = append(body, m.rowLines(i, w)...)
			body = append(body, "")
		}
	}
	if m.err != "" && time.Since(m.errAt) < 6*time.Second {
		msg := ansi.Truncate(m.err, w-2, "…")
		body = append(body[:min(len(body), max(bodyH-1, 0))], " "+sDel.Render(msg))
	}
	for len(body) < bodyH {
		body = append(body, "")
	}
	b.WriteString(strings.Join(body[:max(bodyH, 0)], "\n") + "\n")

	b.WriteString(sFaint.Render(strings.Repeat("─", w)) + "\n")
	hints := " " + sKey.Render(app.LeaderLabel()) + sDim.Render(" menu ") + sKey.Render("n") + sDim.Render(" new ") + sKey.Render("x") + sDim.Render(" close ") + sKey.Render(".") + sDim.Render(" next ") + sKey.Render("?")
	b.WriteString(ansi.Truncate(hints, w, ""))
	return b.String()
}

func (m *Sidebar) rowLines(i, w int) []string {
	r := m.rows[i]
	selected := i == m.cursor
	bar := " "
	if selected {
		bar = sSelBar.Render("▌")
	}
	inner := w - 3

	idx := sDim.Render(strconv.Itoa(i + 1))
	if i >= 9 {
		idx = " "
	}
	name := sText.Render(ansi.Truncate(r.s.Name, inner-2, "…"))
	if selected {
		name = sName.Foreground(cAccent).Render(ansi.Truncate(r.s.Name, inner-2, "…"))
	}
	line1 := idx + " " + name

	line2 := spread(m.statusLabel(r), sDim.Render(app.ModelLabel(m.cfg, r.s.Model)), inner)

	updated := r.st.Updated
	if updated.IsZero() {
		updated = r.s.CreatedAt
	}
	line3 := spread(m.diffLabel(r.s.ID), sDim.Render(age(updated)), inner)

	line4 := ""
	if r.st.Prompt != "" {
		line4 = sFaint.Render("› ") + sPrompt.Render(ansi.Truncate(r.st.Prompt, inner-2, "…"))
	}

	return []string{
		bar + " " + line1,
		bar + " " + line2,
		bar + " " + line3,
		bar + " " + line4,
	}
}

func (m *Sidebar) statusLabel(r row) string {
	if r.gone {
		return sExited.Render("✕ exited") + sDim.Render(" · R restarts")
	}
	switch r.st.State {
	case store.StateWorking:
		return m.spin.View() + sWorking.Render(" working")
	case store.StateTool:
		tool := r.st.Tool
		if tool == "" {
			tool = "tool"
		}
		return m.spin.View() + sWorking.Render(" "+ansi.Truncate(tool, 14, "…"))
	case store.StateNeedsYou:
		return sNeeds.Render("◆ needs you")
	case store.StateReady:
		if r.unseen() {
			return sUnseen.Render("● done")
		}
		if r.st.Message == "interrupted" {
			return sReady.Render("● ") + sDim.Render("interrupted")
		}
		return sReady.Render("● ready")
	case store.StateExited:
		return sExited.Render("✕ exited")
	default:
		return sDim.Render("○ new")
	}
}

func (m *Sidebar) diffLabel(id string) string {
	d, ok := m.diffs[id]
	if !ok {
		return ""
	}
	if d.Files == 0 && d.Commits == 0 {
		return sFaint.Render("no changes")
	}
	parts := []string{sAdd.Render(fmt.Sprintf("+%d", d.Added)) + " " + sDel.Render(fmt.Sprintf("−%d", d.Deleted))}
	if d.Files > 0 {
		parts = append(parts, sDim.Render(fmt.Sprintf("%df", d.Files)))
	}
	if d.Commits > 0 {
		parts = append(parts, sDim.Render(fmt.Sprintf("↑%d", d.Commits)))
	}
	return strings.Join(parts, sFaint.Render(" · "))
}

func (m *Sidebar) helpLines(w int) []string {
	keys := [][2]string{
		{"j/k ↑/↓", "switch session"},
		{"1–9", "jump to session"},
		{".", "next needing you"},
		{"enter", "focus session"},
		{"n", "new session"},
		{"r", "rename"},
		{"x", "close + remove worktree"},
		{"R", "restart exited session"},
		{"q", "detach (keeps running)"},
		{"", ""},
		{"anywhere", ""},
		{app.LeaderLabel(), "menu: new, close, switch,"},
		{"", "detach, quit"},
		{"", ""},
		{"with Option as Meta", ""},
		{"M-n / M-x", "new / close"},
		{"M-j/M-k", "next / prev"},
		{"M-.", "next needing you"},
		{"M-s / M-q", "sidebar / detach"},
	}
	out := []string{}
	for _, k := range keys {
		if k[1] == "" {
			out = append(out, " "+sDim.Render(k[0]))
			continue
		}
		if k[0] == "" {
			out = append(out, " "+strings.Repeat(" ", 10)+sDim.Render(k[1]))
			continue
		}
		out = append(out, ansi.Truncate(" "+sKey.Render(fmt.Sprintf("%-10s", k[0]))+sDim.Render(k[1]), w, "…"))
	}
	return out
}

// spread lays out left and right on one line of width w, truncating left if needed.
func spread(left, right string, w int) string {
	rw := lipgloss.Width(right)
	if lipgloss.Width(left)+rw+1 > w {
		left = ansi.Truncate(left, max(w-rw-1, 0), "…")
	}
	gap := max(w-lipgloss.Width(left)-rw, 1)
	return left + strings.Repeat(" ", gap) + right
}

// reconcile corrects hook state from the screen when hooks went quiet: a turn
// cancelled with Esc sends no Stop, and a menu may be up before Notification fires.
func (m *Sidebar) reconcile() tea.Cmd {
	var cmds []tea.Cmd
	live := map[string]bool{}
	for _, r := range m.rows {
		id := r.s.ID
		live[id] = true
		if r.screen != agent.ScreenIdle && r.screen != agent.ScreenWaiting {
			delete(m.hits, id)
			continue
		}
		h := m.hits[id]
		if h.screen != r.screen {
			h = screenHits{screen: r.screen}
		}
		h.n++
		m.hits[id] = h
		if (r.screen == agent.ScreenIdle && h.n < idleHits) || (r.screen == agent.ScreenWaiting && h.n < waitingHits) {
			continue
		}
		delete(m.hits, id)
		sess, read, screen := r.s, r.st.Updated, r.screen
		cmds = append(cmds, func() tea.Msg {
			if screen == agent.ScreenWaiting {
				if _, ok := store.UpdateStatusIf(sess.ID, read, func(s *store.Status) {
					s.State, s.Tool, s.Message = store.StateNeedsYou, "", "waiting for your answer"
				}); ok {
					app.Alert(sess, notify.NeedsYou, "waiting for your answer", "")
				}
			} else {
				// Interrupted: the user was there to press Esc, so it is not "unseen".
				store.UpdateStatusIf(sess.ID, read, func(s *store.Status) {
					s.State, s.Tool, s.Message = store.StateReady, "", "interrupted"
				})
			}
			return load()
		})
	}
	for id := range m.hits {
		if !live[id] {
			delete(m.hits, id)
		}
	}
	return tea.Batch(cmds...)
}

// summary is the header's attention tally: waiting on you, unseen, running.
func (m *Sidebar) summary() string {
	var needs, unseen, busy int
	for _, r := range m.rows {
		switch {
		case r.gone:
		case r.st.State == store.StateNeedsYou:
			needs++
		case r.unseen():
			unseen++
		case r.st.Busy():
			busy++
		}
	}
	var parts []string
	if needs > 0 {
		parts = append(parts, sNeeds.Render(fmt.Sprintf("◆%d", needs)))
	}
	if unseen > 0 {
		parts = append(parts, sUnseen.Render(fmt.Sprintf("●%d", unseen)))
	}
	if busy > 0 {
		parts = append(parts, sWorking.Render(fmt.Sprintf("●%d", busy)))
	}
	if len(parts) == 0 && len(m.rows) > 0 {
		return sDim.Render(strconv.Itoa(len(m.rows)))
	}
	return strings.Join(parts, " ")
}
