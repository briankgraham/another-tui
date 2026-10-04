// ctabs runs Claude Code sessions side by side, each in its own git worktree,
// with a sidebar of tabs showing what every session is doing.
package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"

	"ctabs/internal/agent"
	"ctabs/internal/app"
	"ctabs/internal/notify"
	"ctabs/internal/store"
	"ctabs/internal/tmux"
	"ctabs/internal/ui"
	"ctabs/internal/worktree"
)

const usage = `ctabs: tabbed Claude Code sessions in git worktrees

usage:
  ctabs            start or attach (run inside a git repo)
  ctabs new        new-session form
  ctabs close [id] close a session and remove its worktree
  ctabs switch next|prev|N
  ctabs kill       stop the ctabs tmux server (sessions resume on next start)

In ctabs, press Ctrl+] anywhere for the menu (new, close, switch, detach, quit).

internal: sidebar, placeholder, menu, rename <id>, hook <id> <event>
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "ctabs:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cmd := ""
	if len(args) > 0 {
		cmd = args[0]
	}
	arg := func(i int) string {
		if len(args) > i {
			return args[i]
		}
		return ""
	}
	switch cmd {
	case "":
		return startOrAttach()
	case "sidebar":
		_, err := tea.NewProgram(ui.NewSidebar(), tea.WithAltScreen(), tea.WithMouseCellMotion(), tea.WithReportFocus()).Run()
		return err
	case "placeholder":
		_, err := tea.NewProgram(ui.Placeholder{}, tea.WithAltScreen()).Run()
		return err
	case "hook":
		hook(arg(1), arg(2))
		return nil
	case "switch":
		if err := app.Switch(arg(1)); err != nil {
			_, _ = tmux.Run("display-message", "ctabs: "+err.Error())
		}
		return nil
	case "new":
		return ui.RunNew()
	case "close":
		return ui.RunClose(arg(1))
	case "rename":
		return ui.RunRename(arg(1))
	case "menu":
		if err := app.Menu(arg(1)); err != nil {
			_, _ = tmux.Run("display-message", "ctabs: "+err.Error())
		}
		return nil
	case "kill":
		_, err := tmux.Run("kill-server")
		return err
	case "-h", "--help", "help":
		fmt.Print(usage)
		return nil
	default:
		fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("unknown command %q", cmd)
	}
}

func startOrAttach() error {
	if _, err := exec.LookPath("claude"); err != nil {
		return fmt.Errorf("the claude CLI is not on PATH")
	}
	cwd, _ := os.Getwd()
	repo, _ := worktree.RepoRoot(cwd)
	if err := app.Bootstrap(repo); err != nil {
		return err
	}
	bin, err := exec.LookPath("tmux")
	if err != nil {
		return err
	}
	// Attach in place of this process; drop $TMUX so this also works from inside tmux.
	env := []string{}
	for _, e := range os.Environ() {
		if len(e) < 5 || e[:5] != "TMUX=" {
			env = append(env, e)
		}
	}
	return syscall.Exec(bin, []string{"tmux", "-L", tmux.Socket, "attach-session", "-t", tmux.MainSession}, env)
}

// hook records a Claude Code hook event. It must never fail or print to stdout,
// since that would surface inside the agent session.
func hook(id, event string) {
	if id == "" || event == "" {
		return
	}
	payload, _ := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	sess := store.Session{ID: id, Agent: "claude"}
	if st, err := store.Load(); err == nil {
		if _, s := st.Find(id); s != nil {
			sess = *s
		}
	}
	a, err := agent.Get(sess.Agent)
	if err != nil {
		return
	}
	prev, _ := store.LoadStatus(id)
	next := a.MapHook(event, payload, prev)
	if store.SaveStatus(id, next) != nil {
		return
	}
	switch {
	case next.State == store.StateNeedsYou && prev.State != store.StateNeedsYou:
		app.Alert(sess, notify.NeedsYou, firstNonEmpty(next.Message, "waiting for you"), "")
	case next.State == store.StateReady && prev.Busy():
		app.Alert(sess, notify.Done, "finished: "+firstNonEmpty(next.Prompt, "turn complete"), next.PR)
	}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
