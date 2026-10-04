package ui

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"ctabs/internal/repos"
)

// The repo picker has two modes: "find" fuzzy-searches recent repositories and
// ones found on disk; "browse" walks folders like a file manager.
type pickMode int

const (
	modeFind pickMode = iota
	modeBrowse
)

type pickEntry struct {
	path   string // absolute
	label  string // what is matched and shown
	repo   bool
	recent bool
	header string // non-empty for a section heading row
	pos    []int  // matched rune positions in label
	score  int
}

type scanDoneMsg []string

type repoPicker struct {
	mode     pickMode
	input    textinput.Model
	w, h     int
	cursor   int
	offset   int
	entries  []pickEntry
	recent   []string
	found    []string
	scanning bool
	dir      string // browse mode
	branches map[string]string
	notice   string
	roots    []string

	chosen string
	quit   bool
}

// PickRepo runs the repository picker full screen and returns the chosen
// repository, or "" if cancelled. initial is offered first.
func PickRepo(initial string, roots []string) (string, error) {
	m := newRepoPicker(initial, roots)
	out, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	if err != nil {
		return "", err
	}
	return out.(*repoPicker).chosen, nil
}

func newRepoPicker(initial string, roots []string) *repoPicker {
	in := textinput.New()
	in.Prompt = ""
	in.Placeholder = "type to search, or a path like ~/code"
	in.Focus()

	var recent []string
	seen := map[string]bool{}
	add := func(p string) {
		if p != "" && !seen[p] && repos.IsRepo(p) {
			seen[p] = true
			recent = append(recent, p)
		}
	}
	add(initial)
	for _, p := range repos.Recent() {
		add(p)
	}

	m := &repoPicker{
		input:    in,
		recent:   recent,
		found:    repos.Cached(),
		scanning: true,
		branches: map[string]string{},
		dir:      browseStart(initial),
	}
	m.roots = roots
	m.refresh()
	return m
}

func browseStart(initial string) string {
	if initial != "" {
		return filepath.Dir(initial)
	}
	if home, err := os.UserHomeDir(); err == nil {
		return home
	}
	return "/"
}

func (m *repoPicker) Init() tea.Cmd {
	roots := m.roots
	return tea.Batch(textinput.Blink, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		found := repos.Discover(ctx, roots, 4)
		_ = repos.SaveCache(found)
		return scanDoneMsg(found)
	})
}

func (m *repoPicker) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.input.Width = m.w - 8
		m.clampScroll()
		return m, nil
	case scanDoneMsg:
		m.found, m.scanning = msg, false
		if m.mode == modeFind {
			m.refresh()
		}
		return m, nil
	case tea.KeyMsg:
		m.notice = ""
		switch msg.String() {
		case "esc", "ctrl+c":
			if m.mode == modeBrowse && m.input.Value() != "" {
				m.setQuery("")
				return m, nil
			}
			m.quit = true
			return m, tea.Quit
		case "up", "ctrl+p", "ctrl+k":
			m.move(-1)
			return m, nil
		case "down", "ctrl+n", "ctrl+j":
			m.move(1)
			return m, nil
		case "pgup":
			m.move(-m.listHeight())
			return m, nil
		case "pgdown":
			m.move(m.listHeight())
			return m, nil
		case "tab":
			m.toggleMode()
			return m, nil
		case "enter":
			return m.enter()
		case "right":
			if m.mode == modeBrowse && m.input.Position() == len(m.input.Value()) {
				if e, ok := m.current(); ok {
					m.openDir(e.path)
				}
				return m, nil
			}
		case "left":
			if m.mode == modeBrowse && m.input.Value() == "" {
				m.openDir(filepath.Dir(m.dir))
				return m, nil
			}
		case "backspace":
			if m.mode == modeBrowse && m.input.Value() == "" {
				m.openDir(filepath.Dir(m.dir))
				return m, nil
			}
		case "/":
			// In browse mode "/" completes into the highlighted folder, like a shell.
			if m.mode == modeBrowse && m.input.Value() != "" {
				if e, ok := m.current(); ok {
					m.openDir(e.path)
				}
				return m, nil
			}
		case "~":
			if m.input.Value() == "" {
				if home, err := os.UserHomeDir(); err == nil {
					m.mode = modeBrowse
					m.openDir(home)
				}
				return m, nil
			}
		}
	}

	before := m.input.Value()
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	if v := m.input.Value(); v != before {
		m.onQuery(v)
	}
	return m, cmd
}

// onQuery reacts to typing. A query containing a path (typed or pasted)
// navigates the folder browser there and filters by whatever follows the last
// slash; in search mode this only happens for absolute or ~/ paths.
func (m *repoPicker) onQuery(v string) {
	abs := strings.HasPrefix(v, "/") || strings.HasPrefix(v, "~/")
	if abs || (m.mode == modeBrowse && strings.Contains(v, "/")) {
		p := repos.Expand(v)
		if !abs {
			p = filepath.Join(m.dir, v)
		}
		dir, rest := filepath.Dir(p), filepath.Base(p)
		if strings.HasSuffix(v, "/") {
			dir, rest = filepath.Clean(p), ""
		}
		if dir = m.resolveDir(dir); dir != "" {
			m.mode = modeBrowse
			m.dir = dir
			m.setQuery(rest)
			return
		}
	}
	m.cursor, m.offset = 0, 0
	m.refresh()
}

// resolveDir returns dir if it exists. Otherwise each missing path element is
// fuzzy-matched against the folders that do exist, so "~/doc/tui" finds
// ~/Documents/tui-for-multiple-ais. It returns "" if nothing fits.
func (m *repoPicker) resolveDir(dir string) string {
	if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
		return dir
	}
	parent := filepath.Dir(dir)
	if parent == dir {
		return ""
	}
	if parent = m.resolveDir(parent); parent == "" {
		return ""
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		return ""
	}
	want, best, bestScore := filepath.Base(dir), "", -1<<31
	for _, e := range entries {
		if !e.IsDir() || (strings.HasPrefix(e.Name(), ".") && !strings.HasPrefix(want, ".")) {
			continue
		}
		if score, _, ok := repos.Match(want, e.Name()); ok && score > bestScore {
			best, bestScore = filepath.Join(parent, e.Name()), score
		}
	}
	return best
}

func (m *repoPicker) setQuery(v string) {
	m.input.SetValue(v)
	m.input.CursorEnd()
	m.cursor, m.offset = 0, 0
	m.refresh()
}

func (m *repoPicker) toggleMode() {
	if m.mode == modeBrowse {
		m.mode = modeFind
		m.setQuery("")
		return
	}
	// Open the browser in the highlighted repo's folder, with that repo selected.
	from := ""
	if e, ok := m.current(); ok {
		from = e.path
		m.dir = filepath.Dir(e.path)
	}
	m.mode = modeBrowse
	m.setQuery("")
	m.selectPath(from)
}

// selectPath moves the cursor to the entry for path, if it is listed.
func (m *repoPicker) selectPath(path string) {
	for i, e := range m.entries {
		if path != "" && e.path == path {
			m.cursor = i
			m.clampScroll()
			return
		}
	}
}

func (m *repoPicker) enter() (tea.Model, tea.Cmd) {
	e, ok := m.current()
	if !ok {
		return m, nil
	}
	if e.repo {
		m.chosen = e.path
		_ = repos.Touch(e.path)
		return m, tea.Quit
	}
	if m.mode == modeBrowse {
		m.openDir(e.path)
	}
	return m, nil
}

func (m *repoPicker) openDir(dir string) {
	if _, err := os.ReadDir(dir); err != nil {
		m.notice = "can't open " + repos.Tilde(dir)
		return
	}
	prev := m.dir
	m.dir = dir
	m.setQuery("")
	// Going up: keep the folder we came out of highlighted.
	m.selectPath(prev)
}

func (m *repoPicker) current() (pickEntry, bool) {
	if m.cursor < 0 || m.cursor >= len(m.entries) || m.entries[m.cursor].header != "" {
		return pickEntry{}, false
	}
	return m.entries[m.cursor], true
}

func (m *repoPicker) move(d int) {
	if len(m.entries) == 0 {
		return
	}
	step := 1
	if d < 0 {
		step = -1
	}
	n := m.cursor + d
	n = max(0, min(n, len(m.entries)-1))
	// Skip section headings.
	for n >= 0 && n < len(m.entries) && m.entries[n].header != "" {
		n += step
	}
	if n < 0 || n >= len(m.entries) {
		return
	}
	m.cursor = n
	m.clampScroll()
}

// refresh rebuilds the visible list for the current mode and query.
func (m *repoPicker) refresh() {
	q := strings.TrimSpace(m.input.Value())
	if m.mode == modeFind {
		m.entries = m.findEntries(q)
	} else {
		m.entries = m.browseEntries(q)
	}
	if m.cursor >= len(m.entries) {
		m.cursor = len(m.entries) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	if len(m.entries) > 0 && m.entries[m.cursor].header != "" {
		m.move(1)
	}
	m.clampScroll()
}

func (m *repoPicker) findEntries(q string) []pickEntry {
	isRecent := map[string]bool{}
	for _, p := range m.recent {
		isRecent[p] = true
	}
	var others []string
	for _, p := range m.found {
		if !isRecent[p] {
			others = append(others, p)
		}
	}
	mk := func(p string, recent bool) pickEntry {
		return pickEntry{path: p, label: repos.Tilde(p), repo: true, recent: recent}
	}

	if q == "" {
		var out []pickEntry
		if len(m.recent) > 0 {
			out = append(out, pickEntry{header: "Recent"})
			for _, p := range m.recent {
				out = append(out, mk(p, true))
			}
		}
		if len(others) > 0 {
			out = append(out, pickEntry{header: "On this machine"})
			for _, p := range others {
				out = append(out, mk(p, false))
			}
		}
		return out
	}

	var out []pickEntry
	for _, group := range [][]string{m.recent, others} {
		for _, p := range group {
			e := mk(p, isRecent[p])
			score, pos, ok := repos.Match(q, e.label)
			if !ok {
				continue
			}
			if e.recent {
				score += 10
			}
			e.score, e.pos = score, pos
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].score > out[j].score })
	return out
}

func (m *repoPicker) browseEntries(q string) []pickEntry {
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return nil
	}
	showHidden := strings.HasPrefix(q, ".")
	var out []pickEntry
	for _, d := range entries {
		n := d.Name()
		if !d.IsDir() {
			// Follow symlinks to folders.
			if d.Type()&os.ModeSymlink == 0 {
				continue
			}
			if fi, err := os.Stat(filepath.Join(m.dir, n)); err != nil || !fi.IsDir() {
				continue
			}
		}
		if strings.HasPrefix(n, ".") && !showHidden {
			continue
		}
		p := filepath.Join(m.dir, n)
		e := pickEntry{path: p, label: n, repo: repos.IsRepo(p)}
		if q != "" {
			score, pos, ok := repos.Match(q, n)
			if !ok {
				continue
			}
			e.score, e.pos = score, pos
		}
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if q != "" && out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		if out[i].repo != out[j].repo {
			return out[i].repo // repositories first
		}
		return strings.ToLower(out[i].label) < strings.ToLower(out[j].label)
	})
	return out
}

func (m *repoPicker) listHeight() int {
	// title, blank, input, rule ... rule, help
	return max(1, m.h-7)
}

func (m *repoPicker) clampScroll() {
	h := m.listHeight()
	if m.cursor < m.offset {
		m.offset = m.cursor
		// Keep a section heading visible above the first row.
		if m.offset > 0 && m.entries[m.offset-1].header != "" {
			m.offset--
		}
	}
	if m.cursor >= m.offset+h {
		m.offset = m.cursor - h + 1
	}
	m.offset = max(0, min(m.offset, max(0, len(m.entries)-h)))
}

func (m *repoPicker) branch(p string) string {
	b, ok := m.branches[p]
	if !ok {
		b = repos.Branch(p)
		m.branches[p] = b
	}
	return b
}

func (m *repoPicker) View() string {
	if m.quit || m.chosen != "" || m.w == 0 {
		return ""
	}
	w := m.w - 4
	var b strings.Builder

	// Title with mode tabs.
	tab := func(label string, on bool) string {
		if on {
			return sKey.Render(label)
		}
		return sDim.Render(label)
	}
	b.WriteString(sTitle.Render("Repository") + "   " +
		tab("Search", m.mode == modeFind) + sFaint.Render(" │ ") + tab("Browse", m.mode == modeBrowse) +
		sFaint.Render("   tab switches") + "\n\n")

	if m.mode == modeBrowse {
		b.WriteString(sDim.Render(ansi.Truncate(repos.Tilde(m.dir)+"/", w, "…")) + "\n")
	} else {
		b.WriteString("\n")
	}
	b.WriteString(sKey.Render("❯ ") + m.input.View() + "\n")
	b.WriteString(sFaint.Render(strings.Repeat("─", w)) + "\n")

	h := m.listHeight() - 1
	end := min(len(m.entries), m.offset+h)
	for i := m.offset; i < end; i++ {
		b.WriteString(m.row(m.entries[i], i == m.cursor, w) + "\n")
	}
	if len(m.entries) == 0 {
		switch {
		case m.mode == modeFind && m.scanning:
			b.WriteString(sDim.Render("  looking for repositories…") + "\n")
		case m.mode == modeFind:
			b.WriteString(sDim.Render("  no matches · tab to browse folders") + "\n")
		default:
			b.WriteString(sDim.Render("  no folders here") + "\n")
		}
		end++
	}
	for i := end - m.offset; i < h; i++ {
		b.WriteString("\n")
	}

	b.WriteString(sFaint.Render(strings.Repeat("─", w)) + "\n")
	b.WriteString(m.footer())
	return lipgloss.NewStyle().Padding(1, 2, 0, 2).Render(b.String())
}

func (m *repoPicker) footer() string {
	if m.notice != "" {
		return sDel.Render(m.notice)
	}
	k := func(key, what string) string { return sKey.Render(key) + " " + sDim.Render(what) }
	var parts []string
	if m.mode == modeFind {
		parts = []string{k("enter", "choose"), k("↑↓", "move"), k("tab", "browse"), k("esc", "cancel")}
		if m.scanning {
			parts = append(parts, sDim.Render("scanning…"))
		}
	} else {
		parts = []string{k("enter", "choose/open"), k("←", "up"), k("→", "open"), k("tab", "search"), k("esc", "cancel")}
	}
	return strings.Join(parts, "  ")
}

func (m *repoPicker) row(e pickEntry, sel bool, w int) string {
	if e.header != "" {
		return sDim.Render(e.header)
	}
	bar := "  "
	if sel {
		bar = sSelBar.Render("▌ ")
	}

	var icon, name, detail string
	if e.repo {
		icon = sReady.Render("⎇ ")
	} else {
		icon = sDim.Render("▸ ")
	}
	if m.mode == modeFind {
		// Show the folder name prominently and the full path after it.
		base := filepath.Base(e.path)
		name = highlight(e.label, e.pos, len([]rune(e.label))-len([]rune(base)), sDim, sName)
		detail = m.branch(e.path)
	} else {
		st := sText
		if e.repo {
			st = sName
		}
		name = highlight(e.label, e.pos, 0, st, st)
		if !e.repo {
			name += sDim.Render("/")
		} else {
			detail = m.branch(e.path)
		}
	}
	line := bar + icon + name
	if detail != "" {
		room := w - lipgloss.Width(line) - 2
		if room > 4 {
			detail = ansi.Truncate(detail, room, "…")
			line += strings.Repeat(" ", room-lipgloss.Width(detail)+2) + sDim.Render(detail)
		}
	}
	line = ansi.Truncate(line, w, "…")
	if sel {
		return sSelected.Width(w).Render(line)
	}
	return line
}

// highlight renders s with matched runes accented. Runes before split use
// pre, the rest use post.
func highlight(s string, pos []int, split int, pre, post lipgloss.Style) string {
	match := map[int]bool{}
	for _, p := range pos {
		match[p] = true
	}
	var b strings.Builder
	for i, r := range []rune(s) {
		st := post
		if i < split {
			st = pre
		}
		if match[i] {
			st = sKey
		}
		b.WriteString(st.Render(string(r)))
	}
	return b.String()
}
