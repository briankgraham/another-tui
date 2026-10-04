package agent

import (
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"ctabs/internal/store"
)

func init() { register(claude{}) }

type claude struct{}

func (claude) Name() string { return "claude" }

func (claude) DefaultModels() []Model {
	return []Model{
		{ID: "opus", Label: "Opus"},
		{ID: "sonnet", Label: "Sonnet"},
		{ID: "haiku", Label: "Haiku"},
		{ID: "claude-fable-5-1", Label: "Fable"},
		{ID: "", Label: "CLI default"},
	}
}

func (claude) Command(o LaunchOpts) []string {
	args := []string{"claude"}
	if o.Model != "" {
		args = append(args, "--model", o.Model)
	}
	if o.SettingsPath != "" {
		args = append(args, "--settings", o.SettingsPath)
	}
	if o.Resume {
		args = append(args, "--resume", o.SessionID)
	} else {
		args = append(args, "--session-id", o.SessionID)
		if o.Prompt != "" {
			// "--" so a prompt like "-p foo" isn't parsed as a flag.
			args = append(args, "--", o.Prompt)
		}
	}
	return args
}

// hookEvents are the Claude Code lifecycle events ctabs listens to.
var hookEvents = []string{
	"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "Notification", "Stop", "SessionEnd",
}

func (claude) HookSettings(hookCmd func(event string) string) ([]byte, error) {
	type hook struct {
		Type    string `json:"type"`
		Command string `json:"command"`
	}
	type matcher struct {
		Matcher string `json:"matcher,omitempty"`
		Hooks   []hook `json:"hooks"`
	}
	hooks := map[string][]matcher{}
	for _, ev := range hookEvents {
		m := matcher{Hooks: []hook{{Type: "command", Command: hookCmd(ev)}}}
		if ev == "PreToolUse" || ev == "PostToolUse" {
			m.Matcher = "*"
		}
		hooks[ev] = []matcher{m}
	}
	return json.MarshalIndent(map[string]any{"hooks": hooks}, "", "  ")
}

type hookPayload struct {
	SessionID        string          `json:"session_id"`
	TranscriptPath   string          `json:"transcript_path"`
	ToolName         string          `json:"tool_name"`
	Message          string          `json:"message"`
	NotificationType string          `json:"notification_type"`
	Prompt           string          `json:"prompt"`
	ToolResponse     json.RawMessage `json:"tool_response"`
	Reason           string          `json:"reason"` // SessionEnd: clear, logout, prompt_input_exit, other
}

func (claude) MapHook(event string, payload []byte, prev store.Status) store.Status {
	var p hookPayload
	_ = json.Unmarshal(payload, &p) // a malformed payload still updates state

	s := prev
	s.Tool, s.Message, s.Updated = "", "", time.Now()
	if p.TranscriptPath != "" {
		s.TranscriptPath = p.TranscriptPath
	}
	// /clear and /resume switch conversations inside the same process; resume
	// must follow whichever one is current.
	if p.SessionID != "" {
		s.AgentSessionID = p.SessionID
	}
	switch event {
	case "SessionStart":
		// The process is alive; keep the current state unless it was marked exited.
		s.Tool, s.Message = prev.Tool, prev.Message
		if prev.State == store.StateExited {
			s.State = store.StateReady
		}
	case "UserPromptSubmit":
		s.State, s.PR = store.StateWorking, ""
		if p := strings.Join(strings.Fields(p.Prompt), " "); p != "" {
			s.Prompt = p
		}
	case "PostToolUse":
		s.State = store.StateWorking
		// `gh pr create` and the GitHub MCP tools both print the new PR's URL.
		if m := prURL.FindAll(p.ToolResponse, -1); len(m) > 0 {
			s.PR = string(m[len(m)-1])
		}
	case "PreToolUse":
		s.State, s.Tool = store.StateTool, p.ToolName
	case "Notification":
		if isIdleNotification(p) {
			// Claude nags after sitting idle; that is not a new request for attention.
			if prev.State == store.StateReady || prev.State == store.StateNew {
				return prev
			}
			s.State = store.StateReady
		} else {
			s.State, s.Message = store.StateNeedsYou, p.Message
		}
	case "Stop":
		s.State, s.Finished = store.StateReady, s.Updated
	case "SessionEnd":
		if p.Reason == "clear" {
			// The process carries on with a new conversation; SessionStart follows.
			s.State, s.Tool, s.Message = prev.State, prev.Tool, prev.Message
			break
		}
		s.State = store.StateExited
	default:
		return prev
	}
	return s
}

func isIdleNotification(p hookPayload) bool {
	return p.NotificationType == "idle_prompt" ||
		strings.Contains(strings.ToLower(p.Message), "waiting for your input")
}

var prURL = regexp.MustCompile(`https://github\.com/[\w.-]+/[\w.-]+/pull/\d+`)

var (
	// "✶ Kerfuffling… (4s · ↓ 257 tokens)" sits above the input box during a turn.
	claudeBusy = regexp.MustCompile(`(?m)^\S\s+\S.*…\s*\(\d`)
	// Permission dialogs and AskUserQuestion render a numbered menu.
	claudeMenu = regexp.MustCompile(`(?m)^\s*❯ 1\. `)
	// The input box prompt.
	claudePrompt = regexp.MustCompile(`(?m)^❯`)
)

func (claude) Inspect(screen string) Screen {
	switch {
	case claudeMenu.MatchString(screen):
		return ScreenWaiting
	case claudeBusy.MatchString(screen):
		return ScreenBusy
	case claudePrompt.MatchString(screen):
		return ScreenIdle
	default:
		return ScreenUnknown
	}
}
