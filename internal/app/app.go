// Package app ties sessions, worktrees, agents and the tmux layout together.
package app

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"ctabs/internal/agent"
	"ctabs/internal/notify"
	"ctabs/internal/repos"
	"ctabs/internal/store"
	"ctabs/internal/tmux"
	"ctabs/internal/worktree"
)

// Exe is the absolute path of the running ctabs binary, used in tmux bindings and hooks.
func Exe() string {
	p, err := os.Executable()
	if err != nil {
		return "ctabs"
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// Config is the optional ~/.ctabs/config.json.
type Config struct {
	Models        []agent.Model `json:"models"`
	Sounds        *bool         `json:"sounds"`        // default on
	Notifications *bool         `json:"notifications"` // default on
	// Leader is the tmux key that opens the ctabs menu from anywhere.
	Leader string `json:"leader"`
	// RepoRoots are the folders scanned for git repositories in the new-session
	// picker (default: your home folder).
	RepoRoots []string `json:"repo_roots"`
}

const DefaultLeader = "C-]"

func on(b *bool) bool { return b == nil || *b }

func LoadConfig(a agent.Agent) Config {
	var c Config
	if b, err := os.ReadFile(filepath.Join(store.Dir(), "config.json")); err == nil {
		_ = json.Unmarshal(b, &c)
	}
	if len(c.Models) == 0 {
		c.Models = a.DefaultModels()
	}
	if c.Leader == "" {
		c.Leader = DefaultLeader
	}
	if len(c.RepoRoots) == 0 {
		if home, err := os.UserHomeDir(); err == nil {
			c.RepoRoots = []string{home}
		}
	}
	for i, r := range c.RepoRoots {
		c.RepoRoots[i] = repos.Expand(r)
	}
	return c
}

// ModelLabel returns a short display label for a model id.
func ModelLabel(c Config, id string) string {
	for _, m := range c.Models {
		if m.ID == id {
			return m.Label
		}
	}
	if id == "" {
		return "default"
	}
	return id
}

func newID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

type NewOpts struct {
	Name   string
	Repo   string
	Base   string
	Model  string
	Prompt string
}

// CreateSession makes a worktree, launches the agent in a parked pane and puts it on stage.
func CreateSession(o NewOpts) (store.Session, error) {
	a, err := agent.Get("claude")
	if err != nil {
		return store.Session{}, err
	}
	repo, err := worktree.RepoRoot(o.Repo)
	if err != nil {
		return store.Session{}, fmt.Errorf("%s is not a git repository", o.Repo)
	}
	baseRef := o.Base
	if baseRef == "" {
		if baseRef, err = worktree.CurrentBranch(repo); err != nil {
			return store.Session{}, err
		}
	}
	_ = repos.Touch(repo)
	slug := worktree.Slug(o.Name)
	wt, err := worktree.Create(repo, store.WorktreesDir(), slug, baseRef)
	if err != nil {
		return store.Session{}, err
	}

	s := store.Session{
		ID:             newID(),
		Name:           o.Name,
		Agent:          a.Name(),
		Model:          o.Model,
		Repo:           repo,
		Worktree:       wt.Path,
		Branch:         wt.Branch,
		BaseRef:        baseRef,
		BaseCommit:     wt.BaseCommit,
		AgentSessionID: uuid.NewString(),
		CreatedAt:      time.Now(),
	}
	rollback := func(err error) (store.Session, error) {
		_ = worktree.Remove(repo, wt.Path, wt.Branch, true)
		_ = os.Remove(store.HookSettingsPath(s.ID))
		return store.Session{}, err
	}

	if err := writeHookSettings(a, s.ID); err != nil {
		return rollback(err)
	}
	argv := a.Command(agent.LaunchOpts{
		SessionID:    s.AgentSessionID,
		Model:        s.Model,
		Prompt:       o.Prompt,
		SettingsPath: store.HookSettingsPath(s.ID),
	})
	if s.PaneID, err = tmux.NewParkedPane(s.ID, s.Worktree, argv); err != nil {
		return rollback(err)
	}
	if err := store.Update(func(st *store.State) error {
		st.Sessions = append(st.Sessions, s)
		return nil
	}); err != nil {
		_ = tmux.KillPane(s.PaneID)
		return rollback(err)
	}
	return s, tmux.Show(s.PaneID, true)
}

func writeHookSettings(a agent.Agent, id string) error {
	exe := Exe()
	b, err := a.HookSettings(func(event string) string {
		return tmux.Quote(exe) + " hook " + id + " " + event
	})
	if err != nil {
		return err
	}
	return store.WriteAtomic(store.HookSettingsPath(id), b)
}

// launchArgv resumes the agent conversation when one was ever started, else starts fresh.
func launchArgv(s store.Session) ([]string, error) {
	a, err := agent.Get(s.Agent)
	if err != nil {
		return nil, err
	}
	if err := writeHookSettings(a, s.ID); err != nil {
		return nil, err
	}
	st, _ := store.LoadStatus(s.ID)
	id := s.AgentSessionID
	if st.AgentSessionID != "" && hasTranscript(st) {
		id = st.AgentSessionID // the user switched conversations with /clear or /resume
	}
	return a.Command(agent.LaunchOpts{
		SessionID:    id,
		Model:        s.Model,
		SettingsPath: store.HookSettingsPath(s.ID),
		Resume:       hasTranscript(st),
	}), nil
}

// hasTranscript reports whether the agent conversation was ever started; hook
// payloads name the transcript path before the file exists.
func hasTranscript(st store.Status) bool {
	if st.TranscriptPath == "" {
		return false
	}
	_, err := os.Stat(st.TranscriptPath)
	return err == nil
}

// EnsurePane returns a live pane for the session, respawning or recreating it if needed.
func EnsurePane(id string) (string, error) {
	st, err := store.Load()
	if err != nil {
		return "", err
	}
	_, s := st.Find(id)
	if s == nil {
		return "", fmt.Errorf("no session %s", id)
	}
	panes, err := tmux.Panes()
	if err != nil {
		return "", err
	}
	p, ok := panes[s.PaneID]
	if ok && !p.Dead {
		return s.PaneID, nil
	}
	if _, err := os.Stat(s.Worktree); err != nil {
		return "", fmt.Errorf("worktree %s is missing", s.Worktree)
	}
	argv, err := launchArgv(*s)
	if err != nil {
		return "", err
	}
	// Any stale "working" state died with the old process.
	prev, _ := store.LoadStatus(id)
	next := store.Status{State: store.StateNew, TranscriptPath: prev.TranscriptPath, AgentSessionID: prev.AgentSessionID, Updated: time.Now()}
	if hasTranscript(prev) {
		next.State = store.StateReady
	}
	_ = store.SaveStatus(id, next)
	if ok {
		return s.PaneID, tmux.Respawn(s.PaneID, s.Worktree, argv)
	}
	pane, err := tmux.NewParkedPane(s.ID, s.Worktree, argv)
	if err != nil {
		return "", err
	}
	return pane, store.Update(func(st *store.State) error {
		if _, s := st.Find(id); s != nil {
			s.PaneID = pane
		}
		return nil
	})
}

// ShowSession stages the session's pane, recreating it if necessary.
func ShowSession(id string, focus bool) error {
	pane, err := EnsurePane(id)
	if err != nil {
		return err
	}
	return tmux.Show(pane, focus)
}

// Switch stages the next/previous session or the n-th (1-based) one.
func Switch(arg string) error {
	st, err := store.Load()
	if err != nil {
		return err
	}
	n := len(st.Sessions)
	if n == 0 {
		return nil
	}
	stage, _ := tmux.Stage()
	cur, _ := st.FindByPane(stage)
	var target int
	switch arg {
	case "next":
		target = (cur + 1) % n
	case "prev":
		if cur < 0 {
			cur = 0
		}
		target = (cur - 1 + n) % n
	case "attention":
		next, ok := NextNeedingAttention(st, stage)
		if !ok {
			_, _ = tmux.Run("display-message", "ctabs: nothing needs you")
			return nil
		}
		return ShowSession(next, false)
	default:
		i, err := strconv.Atoi(arg)
		if err != nil || i < 1 {
			return fmt.Errorf("bad switch target %q", arg)
		}
		if i > n {
			return nil
		}
		target = i - 1
	}
	return ShowSession(st.Sessions[target].ID, false)
}

// StagedSession returns the session currently on stage, if any.
func StagedSession() (store.Session, bool) {
	st, err := store.Load()
	if err != nil {
		return store.Session{}, false
	}
	stage, _ := tmux.Stage()
	if _, s := st.FindByPane(stage); s != nil {
		return *s, true
	}
	return store.Session{}, false
}

// CloseSeen is what the close popup showed the user. CloseSession re-measures
// after the pane is killed and refuses to delete anything beyond it: the agent
// keeps working while the popup is open.
type CloseSeen struct {
	Dirty, Ignored, Commits int
}

// stale reports what grew since seen, or "" when removing is still safe.
func (s *CloseSeen) stale(sess store.Session, deleteBranch bool) (string, error) {
	if _, err := os.Stat(sess.Worktree); err == nil {
		d, err := worktree.Diff(sess.Worktree, sess.BaseCommit)
		if err != nil {
			return "", fmt.Errorf("couldn't re-check the worktree: %w", err)
		}
		if d.Dirty > s.Dirty {
			return fmt.Sprintf("uncommitted changes grew from %d to %d", s.Dirty, d.Dirty), nil
		}
		ign, err := worktree.Ignored(sess.Worktree)
		if err != nil {
			return "", fmt.Errorf("couldn't re-check ignored files: %w", err)
		}
		if ign > s.Ignored {
			return fmt.Sprintf("ignored files grew from %d to %d", s.Ignored, ign), nil
		}
	}
	if deleteBranch {
		if n, err := worktree.BranchCommits(sess.Repo, sess.BaseCommit, sess.Branch); err == nil && n > s.Commits {
			return fmt.Sprintf("branch commits grew from %d to %d", s.Commits, n), nil
		}
	}
	return "", nil
}

// CloseSession kills the session's pane and removes its worktree. A non-nil seen
// is re-checked once the pane is gone; if more work appeared meanwhile, nothing
// is removed and the session stays (restartable with R).
func CloseSession(id string, deleteBranch bool, seen *CloseSeen) error {
	st, err := store.Load()
	if err != nil {
		return err
	}
	idx, s := st.Find(id)
	if s == nil {
		return fmt.Errorf("no session %s", id)
	}
	if stage, _ := tmux.Stage(); stage != "" && stage == s.PaneID {
		// Hand the stage to a neighbour (or the placeholder) before the pane goes away.
		replacement := ""
		if len(st.Sessions) > 1 {
			next := st.Sessions[(idx+1)%len(st.Sessions)]
			if idx == len(st.Sessions)-1 {
				next = st.Sessions[idx-1]
			}
			replacement, _ = EnsurePane(next.ID)
		}
		if replacement == "" {
			replacement, err = ensurePlaceholder()
			if err != nil {
				return err
			}
		}
		if err := tmux.Show(replacement, false); err != nil {
			return err
		}
	}
	if s.PaneID != "" {
		_ = tmux.KillPane(s.PaneID)
	}
	if seen != nil {
		why, err := seen.stale(*s, deleteBranch)
		if err != nil {
			return err
		}
		if why != "" {
			return fmt.Errorf("%s while the popup was open; nothing was removed", why)
		}
	}
	if err := worktree.Remove(s.Repo, s.Worktree, s.Branch, deleteBranch); err != nil {
		return err
	}
	_ = os.Remove(store.StatusPath(id))
	_ = os.Remove(store.HookSettingsPath(id))
	return store.Update(func(st *store.State) error {
		if i, _ := st.Find(id); i >= 0 {
			st.Sessions = append(st.Sessions[:i], st.Sessions[i+1:]...)
		}
		return nil
	})
}

func Rename(id, name string) error {
	return store.Update(func(st *store.State) error {
		_, s := st.Find(id)
		if s == nil {
			return fmt.Errorf("no session %s", id)
		}
		s.Name = name
		return nil
	})
}

func ensurePlaceholder() (string, error) {
	panes, err := tmux.Panes()
	if err != nil {
		return "", err
	}
	if id := tmux.GetOption(tmux.OptPlaceholder); id != "" {
		if _, ok := panes[id]; ok {
			return id, nil
		}
	}
	home, _ := os.UserHomeDir()
	id, err := tmux.NewParkedPane("placeholder", home, []string{Exe(), "placeholder"})
	if err != nil {
		return "", err
	}
	return id, tmux.SetOption(tmux.OptPlaceholder, id)
}

// Bootstrap starts (or repairs) the ctabs tmux server for repo.
func Bootstrap(repo string) error {
	if !tmux.HasServer() {
		return startServer(repo)
	}
	if repo != "" {
		_ = tmux.SetOption(tmux.OptRepo, repo)
	}
	panes, err := tmux.Panes()
	if err != nil {
		return err
	}
	sb := tmux.GetOption(tmux.OptSidebar)
	// Re-apply settings and bindings so an upgraded binary takes effect on attach.
	if err := configure(Exe(), sb); err != nil {
		return err
	}
	if panes[sb].Dead {
		return tmux.Respawn(sb, repoOrHome(repo), []string{Exe(), "sidebar"})
	}
	return nil
}

func repoOrHome(repo string) string {
	if repo != "" {
		return repo
	}
	home, _ := os.UserHomeDir()
	return home
}

func startServer(repo string) (err error) {
	defer func() {
		// A half-built server would look healthy to the next run (has-session) and
		// skip setup; tear it down so the next attempt starts clean.
		if err != nil {
			_, _ = tmux.Run("kill-server")
		}
	}()
	exe := Exe()
	dir := repoOrHome(repo)
	if _, err := tmux.Run("-f", "/dev/null", "new-session", "-d", "-s", tmux.MainSession, "-n", "ctabs",
		"-x", "200", "-y", "50", "-c", dir, exe, "placeholder"); err != nil {
		return err
	}
	placeholder, err := tmux.Stage()
	if err != nil {
		return err
	}
	if _, err := tmux.Run("new-session", "-d", "-s", tmux.BgSession, "-n", "keep", "tail", "-f", "/dev/null"); err != nil {
		return err
	}
	sidebar, err := tmux.Run("split-window", "-hbd", "-l", strconv.Itoa(tmux.SidebarWidth),
		"-t", placeholder, "-P", "-F", "#{pane_id}", "-c", dir, exe, "sidebar")
	if err != nil {
		return err
	}
	for k, v := range map[string]string{
		tmux.OptSidebar:     sidebar,
		tmux.OptPlaceholder: placeholder,
		tmux.OptRepo:        repo,
	} {
		if err := tmux.SetOption(k, v); err != nil {
			return err
		}
	}
	if err := configure(exe, sidebar); err != nil {
		return err
	}
	// Sessions that fail to come back stay in the sidebar (restartable with R);
	// they must not keep the user from attaching to the rest.
	if err := restoreSessions(); err != nil {
		fmt.Fprintln(os.Stderr, "ctabs:", err)
	}
	return tmux.Focus(sidebar)
}

func configure(exe, sidebar string) error {
	q := tmux.Quote(exe)
	popup := func(w, h, sub string) []string {
		return []string{"display-popup", "-E", "-w", w, "-h", h, q + " " + sub}
	}
	// Cosmetic and version-dependent options: older tmux lacks some (e.g.
	// popup-border-* before 3.3), and ctabs works fine without them.
	for _, c := range [][]string{
		{"set", "-s", "escape-time", "0"},
		{"set", "-s", "extended-keys", "on"},
		{"set", "-s", "focus-events", "on"},
		{"set", "-as", "terminal-features", ",*:RGB"},
		{"set", "-g", "default-terminal", "tmux-256color"},
		{"set", "-g", "set-titles", "on"},
		{"set", "-g", "set-titles-string", "ctabs"},
		{"set", "-g", "pane-border-lines", "single"},
		{"set", "-g", "pane-border-style", "fg=colour238"},
		{"set", "-g", "pane-active-border-style", "fg=colour141"},
		{"set", "-g", "popup-border-lines", "rounded"},
		{"set", "-g", "popup-border-style", "fg=colour141"},
	} {
		_, _ = tmux.Run(c...)
	}
	cmds := [][]string{
		{"set", "-g", "mouse", "on"},
		{"set", "-g", "status", "off"},
		{"set", "-g", "history-limit", "50000"},
		{"set", "-g", "remain-on-exit", "on"},
		{"set-hook", "-g", "client-resized", "resize-pane -t " + sidebar + " -x " + strconv.Itoa(tmux.SidebarWidth)},
		{"set-hook", "-g", "client-attached", "resize-pane -t " + sidebar + " -x " + strconv.Itoa(tmux.SidebarWidth)},
		{"bind", "-n", "M-j", "run-shell", "-b", q + " switch next"},
		{"bind", "-n", "M-k", "run-shell", "-b", q + " switch prev"},
		{"bind", "-n", "M-.", "run-shell", "-b", q + " switch attention"},
		{"bind", "-n", "M-s", "select-pane", "-t", sidebar},
		{"bind", "-n", "M-q", "detach-client"},
		append([]string{"bind", "-n", "M-n"}, popup("76", "30", "new")...),
		append([]string{"bind", "-n", "M-x"}, popup("70", "14", "close")...),
	}
	for i := 1; i <= 9; i++ {
		cmds = append(cmds, []string{"bind", "-n", "M-" + strconv.Itoa(i), "run-shell", "-b", q + " switch " + strconv.Itoa(i)})
	}
	for _, c := range cmds {
		if _, err := tmux.Run(c...); err != nil {
			return err
		}
	}
	return bindLeader(q)
}

// bindLeader binds the configured leader key, falling back to DefaultLeader when
// tmux rejects it. The leader works in every terminal; the Alt keys need
// Option-as-Meta on macOS. The key actually bound is recorded for LeaderLabel.
func bindLeader(q string) error {
	bind := func(key string) error {
		_, err := tmux.Run("bind", "-n", key, "run-shell", "-b", q+" menu '#{client_name}'")
		return err
	}
	key := configuredLeader()
	if err := bind(key); err != nil {
		if key == DefaultLeader {
			return err
		}
		fmt.Fprintf(os.Stderr, "ctabs: invalid leader %q, using %s\n", key, DefaultLeader)
		key = DefaultLeader
		if err := bind(key); err != nil {
			return err
		}
	}
	return tmux.SetOption(tmux.OptLeader, key)
}

// restoreSessions relaunches every saved session after the server (re)starts.
func restoreSessions() error {
	st, err := store.Load()
	if err != nil {
		return err
	}
	// Pane ids from the previous server are meaningless now, and the new server
	// reuses them, so clear them all before creating any pane: a stale id left
	// on one session would otherwise point at another session's pane.
	if err := store.Update(func(st *store.State) error {
		for i := range st.Sessions {
			st.Sessions[i].PaneID = ""
		}
		return nil
	}); err != nil {
		return err
	}
	var missing []string
	var first string
	var errs []error
	for _, s := range st.Sessions {
		if _, err := os.Stat(s.Worktree); errors.Is(err, os.ErrNotExist) {
			missing = append(missing, s.ID)
			continue
		}
		// One broken session must not keep the others from coming back.
		pane, err := EnsurePane(s.ID)
		if err != nil {
			errs = append(errs, fmt.Errorf("restore %s: %w", s.Name, err))
			continue
		}
		if first == "" {
			first = pane
		}
	}
	if len(missing) > 0 {
		if err := store.Update(func(st *store.State) error {
			for _, id := range missing {
				if i, _ := st.Find(id); i >= 0 {
					st.Sessions = append(st.Sessions[:i], st.Sessions[i+1:]...)
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	if first != "" {
		errs = append(errs, tmux.Show(first, false))
	}
	return errors.Join(errs...)
}

// Attention ranks sessions that want a look: waiting on the user first, then
// finished-but-unseen, then still running; oldest first within each group.
func Attention(st store.State) []string {
	type item struct {
		id   string
		rank int
		at   time.Time
	}
	var items []item
	for _, s := range st.Sessions {
		status, _ := store.LoadStatus(s.ID)
		switch {
		case status.State == store.StateNeedsYou:
			items = append(items, item{s.ID, 0, status.Updated})
		case status.Unseen(store.LoadSeen(s.ID)):
			items = append(items, item{s.ID, 1, status.Finished})
		case status.Busy():
			items = append(items, item{s.ID, 2, status.Updated})
		}
	}
	slices.SortStableFunc(items, func(a, b item) int {
		if a.rank != b.rank {
			return a.rank - b.rank
		}
		return a.at.Compare(b.at)
	})
	ids := make([]string, len(items))
	for i, it := range items {
		ids[i] = it.id
	}
	return ids
}

// NextNeedingAttention picks the session after the staged one in attention order.
func NextNeedingAttention(st store.State, stage string) (string, bool) {
	ids := Attention(st)
	if len(ids) == 0 {
		return "", false
	}
	_, cur := st.FindByPane(stage)
	if cur != nil {
		if i := slices.Index(ids, cur.ID); i >= 0 {
			if len(ids) == 1 {
				return "", false // already looking at the only one
			}
			return ids[(i+1)%len(ids)], true
		}
	}
	return ids[0], true
}

// Alert sounds/notifies for a session event unless the user is looking at it.
// A non-empty url is opened when the notification is clicked, and is shown even then.
func Alert(s store.Session, k notify.Kind, body, url string) {
	a, err := agent.Get(s.Agent)
	if err != nil {
		return
	}
	cfg := LoadConfig(a)
	stage, _ := tmux.Stage()
	notify.Alert(k, "ctabs · "+s.Name, body, notify.Options{
		Sound:    on(cfg.Sounds),
		Desktop:  on(cfg.Notifications),
		OnScreen: stage != "" && stage == s.PaneID,
		Focused:  tmux.ClientFocused(),
		URL:      url,
	})
}

func configuredLeader() string {
	a, _ := agent.Get("claude")
	return LoadConfig(a).Leader
}

// leader is the key actually bound, which differs from the config when that was invalid.
func leader() string {
	if l := tmux.GetOption(tmux.OptLeader); l != "" {
		return l
	}
	return configuredLeader()
}

// LeaderLabel renders the leader key the way people read it ("C-]" → "^]").
func LeaderLabel() string {
	l := leader()
	if strings.HasPrefix(l, "C-") {
		return "^" + l[2:]
	}
	return l
}

// Menu pops the ctabs menu on client: actions plus every session, by status.
func Menu(client string) error {
	q := tmux.Quote(Exe())
	popup := func(w, h, sub string) string {
		return fmt.Sprintf(`display-popup -E -w %s -h %s "%s %s"`, w, h, q, sub)
	}
	esc := func(s string) string { return strings.ReplaceAll(s, "#", "##") }

	st, err := store.Load()
	if err != nil {
		return err
	}
	staged, hasStaged := StagedSession()

	items := []string{"New session", "n", popup("76", "30", "new")}
	if hasStaged {
		name := esc(truncate(staged.Name, 24))
		items = append(items,
			"Close "+name+"…", "x", popup("70", "14", "close "+staged.ID),
			"Rename "+name+"…", "r", popup("56", "8", "rename "+staged.ID),
		)
	}
	if len(Attention(st)) > 0 {
		items = append(items, "Next needing you", ".", fmt.Sprintf(`run-shell -b "%s switch attention"`, q))
	}
	if len(st.Sessions) > 0 {
		items = append(items, "")
		for i, s := range st.Sessions {
			key := ""
			if i < 9 {
				key = strconv.Itoa(i + 1)
			}
			status, _ := store.LoadStatus(s.ID)
			label := menuIcon(status, store.LoadSeen(s.ID)) + " " + esc(truncate(s.Name, 28))
			if hasStaged && s.ID == staged.ID {
				label += "  ◂"
			}
			items = append(items, label, key, fmt.Sprintf(`run-shell -b "%s switch %d"`, q, i+1))
		}
	}
	items = append(items, "",
		"Focus sidebar", "s", "select-pane -t "+tmux.GetOption(tmux.OptSidebar),
		"Detach (sessions keep running)", "d", "detach-client",
		"Quit ctabs…", "q", `confirm-before -p "Stop all sessions? They resume next time you run ctabs. (y/n)" kill-server`,
	)
	args := []string{"display-menu", "-T", "#[align=centre] ctabs ", "-x", "C", "-y", "C"}
	if client != "" {
		args = append(args, "-c", client)
	}
	_, err = tmux.Run(append(args, items...)...)
	return err
}

func menuIcon(s store.Status, seen time.Time) string {
	switch {
	case s.State == store.StateNeedsYou:
		return "◆"
	case s.Unseen(seen):
		return "●"
	case s.Busy():
		return "…"
	case s.State == store.StateExited:
		return "✕"
	default:
		return "·"
	}
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
