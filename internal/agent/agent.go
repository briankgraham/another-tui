// Package agent abstracts the coding-agent CLI that runs inside each session.
// Only Claude Code is implemented today; other CLIs plug in by implementing Agent.
package agent

import (
	"fmt"

	"ctabs/internal/store"
)

type Model struct {
	ID    string `json:"id"`    // value passed to the CLI; empty means the CLI default
	Label string `json:"label"` // shown in the picker and sidebar
}

type LaunchOpts struct {
	SessionID    string // agent-side conversation id
	Model        string
	Prompt       string // optional first prompt
	SettingsPath string // hook settings file written by HookSettings
	Resume       bool
}

type Agent interface {
	Name() string
	DefaultModels() []Model
	// Command returns argv for launching the agent in the session's worktree.
	Command(o LaunchOpts) []string
	// HookSettings returns a settings file body that reports status by running
	// hookCmd(event) for each lifecycle event.
	HookSettings(hookCmd func(event string) string) ([]byte, error)
	// MapHook folds a hook event payload into the session's status.
	MapHook(event string, payload []byte, prev store.Status) store.Status
	// Inspect reads a captured pane to catch what hooks miss (e.g. a turn
	// cancelled with Esc never sends Stop).
	Inspect(screen string) Screen
}

type Screen int

const (
	ScreenUnknown Screen = iota // not recognisably the agent's UI
	ScreenBusy                  // a turn is running
	ScreenWaiting               // a menu is waiting on the user
	ScreenIdle                  // at the input prompt
)

var registry = map[string]Agent{}

func register(a Agent) { registry[a.Name()] = a }

func Get(name string) (Agent, error) {
	if name == "" {
		name = "claude"
	}
	a, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("unknown agent %q", name)
	}
	return a, nil
}
