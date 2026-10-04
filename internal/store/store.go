// Package store persists ctabs sessions and per-session status under ~/.ctabs.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Dir returns the ctabs home directory ($CTABS_HOME or ~/.ctabs).
func Dir() string {
	if d := os.Getenv("CTABS_HOME"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".ctabs"
	}
	return filepath.Join(home, ".ctabs")
}

func StatePath() string           { return filepath.Join(Dir(), "state.json") }
func StatusDir() string           { return filepath.Join(Dir(), "status") }
func HooksDir() string            { return filepath.Join(Dir(), "hooks") }
func WorktreesDir() string        { return filepath.Join(Dir(), "worktrees") }
func StatusPath(id string) string { return filepath.Join(StatusDir(), id+".json") }
func SeenPath(id string) string   { return filepath.Join(Dir(), "seen", id) }
func HookSettingsPath(id string) string {
	return filepath.Join(HooksDir(), id+".json")
}

// Session is one agent session living in its own worktree and tmux pane.
type Session struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	Agent          string    `json:"agent"`
	Model          string    `json:"model"`
	Repo           string    `json:"repo"`
	Worktree       string    `json:"worktree"`
	Branch         string    `json:"branch"`
	BaseRef        string    `json:"base_ref"`
	BaseCommit     string    `json:"base_commit"`
	AgentSessionID string    `json:"agent_session_id"`
	PaneID         string    `json:"pane_id"`
	CreatedAt      time.Time `json:"created_at"`
}

type State struct {
	Sessions []Session `json:"sessions"`
}

func (s *State) Find(id string) (int, *Session) {
	for i := range s.Sessions {
		if s.Sessions[i].ID == id {
			return i, &s.Sessions[i]
		}
	}
	return -1, nil
}

func (s *State) FindByPane(pane string) (int, *Session) {
	if pane == "" {
		return -1, nil
	}
	for i := range s.Sessions {
		if s.Sessions[i].PaneID == pane {
			return i, &s.Sessions[i]
		}
	}
	return -1, nil
}

// Load reads the state file without locking; a missing file is an empty state.
func Load() (State, error) {
	var st State
	b, err := os.ReadFile(StatePath())
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return st, fmt.Errorf("parse %s: %w", StatePath(), err)
	}
	return st, nil
}

// Update performs a locked read-modify-write of the state file.
func Update(fn func(*State) error) error {
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(Dir(), "state.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	st, err := Load()
	if err != nil {
		return err
	}
	if err := fn(&st); err != nil {
		return err
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return WriteAtomic(StatePath(), b)
}

// WriteAtomic writes via a temp file + rename so readers never see partial files.
func WriteAtomic(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Status states reported by agent hooks.
const (
	StateNew      = "new"
	StateWorking  = "working"
	StateTool     = "tool"
	StateNeedsYou = "needs_input"
	StateReady    = "ready"
	StateExited   = "exited"
)

// Status is the latest activity of a session, written by `ctabs hook`.
type Status struct {
	State          string    `json:"state"`
	Tool           string    `json:"tool,omitempty"`
	Message        string    `json:"message,omitempty"`
	Prompt         string    `json:"prompt,omitempty"`   // last prompt the user submitted
	Finished       time.Time `json:"finished,omitempty"` // when the last turn completed
	TranscriptPath string    `json:"transcript_path,omitempty"`
	AgentSessionID string    `json:"agent_session_id,omitempty"` // current conversation, as reported by hooks
	PR             string    `json:"pr,omitempty"`               // pull request URL seen during the current turn
	Updated        time.Time `json:"updated"`
}

// Unseen reports a finished turn nobody has looked at since it finished.
func (s Status) Unseen(seen time.Time) bool {
	return s.State == StateReady && !s.Finished.IsZero() && s.Finished.After(seen)
}

// Busy reports a turn in progress.
func (s Status) Busy() bool { return s.State == StateWorking || s.State == StateTool }

func LoadStatus(id string) (Status, error) {
	var s Status
	b, err := os.ReadFile(StatusPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return Status{State: StateNew}, nil
	}
	if err != nil {
		return s, err
	}
	err = json.Unmarshal(b, &s)
	return s, err
}

func SaveStatus(id string, s Status) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return WriteAtomic(StatusPath(id), b)
}

// UpdateStatusIf rewrites a status only if no hook has written it since it was
// read (its Updated time still matches), so hooks always win a race.
func UpdateStatusIf(id string, read time.Time, fn func(*Status)) (Status, bool) {
	cur, err := LoadStatus(id)
	if err != nil || !cur.Updated.Equal(read) {
		return cur, false
	}
	fn(&cur)
	cur.Updated = time.Now()
	return cur, SaveStatus(id, cur) == nil
}

// MarkSeen records that the user has looked at the session now.
func MarkSeen(id string) error {
	return WriteAtomic(SeenPath(id), []byte(time.Now().Format(time.RFC3339Nano)))
}

// LoadSeen returns when the user last looked at the session (zero if never).
func LoadSeen(id string) time.Time {
	b, err := os.ReadFile(SeenPath(id))
	if err != nil {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339Nano, string(b))
	return t
}
