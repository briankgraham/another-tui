// Package notify plays sounds and posts desktop notifications for session events.
package notify

import (
	"os/exec"
	"runtime"
	"strings"
	"syscall"
)

type Kind int

const (
	NeedsYou Kind = iota
	Done
)

type Options struct {
	Sound   bool
	Desktop bool
	// OnScreen: the session is staged; Focused: the terminal window has focus.
	OnScreen bool
	Focused  bool
	// URL is opened when the banner is clicked (macOS needs terminal-notifier).
	URL string
}

// Alert tells the user about a session event, unless they are already looking at it.
func Alert(k Kind, title, body string, o Options) {
	if o.OnScreen && o.Focused {
		return
	}
	if o.Sound {
		playSound(k)
	}
	// Banners only help when the terminal is in the background.
	if o.Desktop && !o.Focused {
		desktop(title, body, o.URL)
	}
}

func playSound(k Kind) {
	switch runtime.GOOS {
	case "darwin":
		name := "Glass"
		if k == NeedsYou {
			name = "Sosumi"
		}
		start("afplay", "/System/Library/Sounds/"+name+".aiff")
	case "linux":
		name := "complete"
		if k == NeedsYou {
			name = "dialog-warning"
		}
		start("canberra-gtk-play", "-i", name)
	}
}

func desktop(title, body, url string) {
	switch runtime.GOOS {
	case "darwin":
		// osascript banners can't open a URL on click; terminal-notifier can.
		if url != "" {
			if _, err := exec.LookPath("terminal-notifier"); err == nil {
				start("terminal-notifier", "-title", title, "-subtitle", linkLabel(url),
					"-message", body, "-open", url)
				return
			}
		}
		script := "display notification " + appleQuote(body) + " with title " + appleQuote(title)
		if url != "" {
			script += " subtitle " + appleQuote(url)
		}
		start("osascript", "-e", script)
	case "linux":
		if url != "" {
			body += "\n" + url
		}
		start("notify-send", title, body)
	}
}

// linkLabel turns a PR URL into "Open PR #123".
func linkLabel(url string) string {
	if i := strings.LastIndex(url, "/pull/"); i >= 0 {
		return "Open PR #" + url[i+len("/pull/"):]
	}
	return "Open link"
}

func appleQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// start runs a helper fully detached: hooks must return immediately, and the
// agent waits on the hook's stdout, so the child must not inherit it.
func start(name string, args ...string) {
	if _, err := exec.LookPath(name); err != nil {
		return
	}
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if cmd.Start() == nil {
		go cmd.Wait()
	}
}
