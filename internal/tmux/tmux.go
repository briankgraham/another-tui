// Package tmux drives the dedicated ctabs tmux server (`tmux -L ctabs`).
//
// Layout: session "main" has one window with the sidebar pane on the left and
// exactly one "stage" pane on the right. Every other agent pane is parked in its
// own window inside the hidden session "bg"; showing a session swaps its pane
// with whatever is on stage.
package tmux

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Socket names the private tmux server; CTABS_SOCKET overrides it so a dev or
// test instance never touches the one you are using.
var Socket = socketName()

func socketName() string {
	if s := os.Getenv("CTABS_SOCKET"); s != "" {
		return s
	}
	return "ctabs"
}

const (
	MainSession = "main"
	BgSession   = "bg"
	MainWindow  = MainSession + ":0"

	OptSidebar     = "@ctabs_sidebar"
	OptPlaceholder = "@ctabs_placeholder"
	OptRepo        = "@ctabs_repo"
	OptLeader      = "@ctabs_leader" // the leader key actually bound
)

func Command(args ...string) *exec.Cmd {
	return exec.Command("tmux", append([]string{"-L", Socket}, args...)...)
}

func Run(args ...string) (string, error) {
	cmd := Command(args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("tmux %s: %s", args[0], msg)
	}
	return strings.TrimRight(out.String(), "\n"), nil
}

func HasServer() bool {
	_, err := Run("has-session", "-t", MainSession)
	return err == nil
}

func GetOption(name string) string {
	v, _ := Run("show-options", "-gqv", name)
	return v
}

func SetOption(name, value string) error {
	_, err := Run("set-option", "-g", name, value)
	return err
}

// Pane is a snapshot of one pane on the ctabs server.
type Pane struct {
	ID      string
	Session string
	Dead    bool
}

// Panes lists all panes on the server keyed by pane id.
func Panes() (map[string]Pane, error) {
	out, err := Run("list-panes", "-a", "-F", "#{pane_id}\t#{session_name}\t#{pane_dead}")
	if err != nil {
		return nil, err
	}
	panes := map[string]Pane{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 3 {
			continue
		}
		panes[f[0]] = Pane{ID: f[0], Session: f[1], Dead: f[2] == "1"}
	}
	return panes, nil
}

// Stage returns the pane currently shown next to the sidebar, if any.
func Stage() (string, error) {
	sidebar := GetOption(OptSidebar)
	out, err := Run("list-panes", "-t", MainWindow, "-F", "#{pane_id}")
	if err != nil {
		return "", err
	}
	for _, id := range strings.Fields(out) {
		if id != sidebar {
			return id, nil
		}
	}
	return "", nil
}

// Show puts pane on stage. Focus stays where it is unless focus is true.
func Show(pane string, focus bool) error {
	stage, err := Stage()
	if err != nil {
		return err
	}
	switch {
	case stage == pane:
	case stage == "":
		// The stage pane was lost (e.g. killed by hand); rebuild the split.
		sidebar := GetOption(OptSidebar)
		if _, err := Run("join-pane", "-d", "-h", "-s", pane, "-t", sidebar); err != nil {
			return err
		}
		ResizeSidebar()
	default:
		if _, err := Run("swap-pane", "-d", "-s", pane, "-t", stage); err != nil {
			return err
		}
	}
	if focus {
		_, err = Run("select-pane", "-t", pane)
	}
	return err
}

// NewParkedPane starts argv in a new hidden window and returns its pane id.
func NewParkedPane(name, dir string, argv []string) (string, error) {
	args := []string{"new-window", "-d", "-P", "-F", "#{pane_id}", "-t", BgSession + ":", "-n", name, "-c", dir}
	return Run(append(args, argv...)...)
}

// Respawn restarts a (dead) pane with a new command in dir.
func Respawn(pane, dir string, argv []string) error {
	_, err := Run(append([]string{"respawn-pane", "-k", "-t", pane, "-c", dir}, argv...)...)
	return err
}

func KillPane(pane string) error {
	_, err := Run("kill-pane", "-t", pane)
	return err
}

func Focus(pane string) error {
	_, err := Run("select-pane", "-t", pane)
	return err
}

const SidebarWidth = 32

func ResizeSidebar() {
	if sb := GetOption(OptSidebar); sb != "" {
		_, _ = Run("resize-pane", "-t", sb, "-x", fmt.Sprint(SidebarWidth))
	}
}

// Popup opens a centered popup running argv on the attached client and waits for it.
func Popup(w, h string, argv []string) error {
	_, err := Run(append([]string{"display-popup", "-E", "-w", w, "-h", h}, argv...)...)
	return err
}

// Quote quotes s for the shell commands tmux runs (bindings, hooks).
func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ClientFocused reports whether any attached client's terminal window has focus
// (needs focus-events, which ctabs enables).
func ClientFocused() bool {
	out, err := Run("list-clients", "-F", "#{client_flags}")
	if err != nil {
		return false
	}
	return strings.Contains(out, "focused")
}

// Capture returns the visible text of a pane.
func Capture(pane string) (string, error) {
	return Run("capture-pane", "-p", "-t", pane)
}
