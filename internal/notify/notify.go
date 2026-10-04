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
		if url != "" {
			// osascript banners can't open a URL on click; terminal-notifier can.
			// Without it, ask with a dialog that opens the link in the default
			// browser. It dismisses itself after a while.
			dialog := []string{"-e", "set r to display dialog " + appleQuote(body+"\n"+url) +
				" with title " + appleQuote(title) +
				` buttons {"Dismiss", "Open PR"} default button "Open PR" giving up after 120`,
				"-e", `if button returned of r is "Open PR" then open location ` + appleQuote(url)}
			if _, err := exec.LookPath("terminal-notifier"); err == nil {
				// terminal-notifier exits non-zero when macOS has denied it notification
				// permission. start() can't see that (and this process exits right
				// after), so the fallback has to live in the detached shell.
				script := `terminal-notifier -title "$1" -subtitle "$2" -message "$3" -open "$4" || { shift 4; exec osascript "$@"; }`
				start("sh", append([]string{"-c", script, "sh", title, linkLabel(url), body, url}, dialog...)...)
				return
			}
			start("osascript", dialog...)
			return
		}
		start("osascript", "-e", "display notification "+appleQuote(body)+" with title "+appleQuote(title))
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
