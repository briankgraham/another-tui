package agent

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"ctabs/internal/store"
)

func TestClaudeMapHook(t *testing.T) {
	c := claude{}
	ready := store.Status{State: store.StateReady}
	cases := []struct {
		event   string
		payload string
		prev    store.Status
		want    string
		tool    string
	}{
		{"UserPromptSubmit", `{"transcript_path":"/t.jsonl"}`, ready, store.StateWorking, ""},
		{"PreToolUse", `{"tool_name":"Edit"}`, ready, store.StateTool, "Edit"},
		{"PostToolUse", `{}`, store.Status{State: store.StateTool}, store.StateWorking, ""},
		{"Notification", `{"message":"Claude needs your permission to use Bash"}`, ready, store.StateNeedsYou, ""},
		{"Notification", `{"message":"Claude is waiting for your input"}`, ready, store.StateReady, ""},
		{"Notification", `{"notification_type":"idle_prompt"}`, store.Status{State: store.StateNeedsYou}, store.StateReady, ""},
		{"Stop", `{}`, store.Status{State: store.StateWorking}, store.StateReady, ""},
		{"SessionEnd", `{}`, ready, store.StateExited, ""},
		{"SubagentStop", `{}`, ready, store.StateReady, ""},
		{"Stop", `not json`, store.Status{State: store.StateWorking}, store.StateReady, ""},
	}
	for _, tc := range cases {
		got := c.MapHook(tc.event, []byte(tc.payload), tc.prev)
		if got.State != tc.want || got.Tool != tc.tool {
			t.Errorf("%s %s: got %+v, want state=%s tool=%s", tc.event, tc.payload, got, tc.want, tc.tool)
		}
	}

	got := c.MapHook("UserPromptSubmit", []byte(`{"transcript_path":"/t.jsonl"}`), ready)
	got = c.MapHook("Stop", []byte(`{}`), got)
	if got.TranscriptPath != "/t.jsonl" {
		t.Errorf("transcript path not carried forward: %+v", got)
	}
}

func TestClaudeMapHookConversationSwitch(t *testing.T) {
	c := claude{}
	s := c.MapHook("UserPromptSubmit", []byte(`{"session_id":"a","transcript_path":"/a.jsonl"}`), store.Status{State: store.StateReady})
	s = c.MapHook("Stop", []byte(`{"session_id":"a"}`), s)

	// /clear: the old conversation ends, but the process keeps running.
	s = c.MapHook("SessionEnd", []byte(`{"session_id":"a","reason":"clear"}`), s)
	if s.State != store.StateReady {
		t.Errorf("/clear marked the session %s", s.State)
	}
	s = c.MapHook("SessionStart", []byte(`{"session_id":"b","transcript_path":"/b.jsonl","source":"clear"}`), s)
	if s.AgentSessionID != "b" || s.TranscriptPath != "/b.jsonl" || s.State != store.StateReady {
		t.Errorf("after /clear: %+v", s)
	}

	// /resume may end with another reason; SessionStart brings the session back.
	s = c.MapHook("SessionEnd", []byte(`{"session_id":"b","reason":"other"}`), s)
	s = c.MapHook("SessionStart", []byte(`{"session_id":"c","transcript_path":"/c.jsonl","source":"resume"}`), s)
	if s.AgentSessionID != "c" || s.State != store.StateReady {
		t.Errorf("after /resume: %+v", s)
	}

	// SessionStart mid-turn (e.g. compact) leaves the turn alone.
	busy := store.Status{State: store.StateTool, Tool: "Bash"}
	if got := c.MapHook("SessionStart", []byte(`{"session_id":"c","source":"compact"}`), busy); got.State != store.StateTool || got.Tool != "Bash" {
		t.Errorf("SessionStart changed a running turn: %+v", got)
	}

	if got := c.MapHook("SessionEnd", []byte(`{"reason":"prompt_input_exit"}`), s); got.State != store.StateExited {
		t.Errorf("real exit: %+v", got)
	}
}

func TestClaudeMapHookPR(t *testing.T) {
	c := claude{}
	s := c.MapHook("UserPromptSubmit", []byte(`{"prompt":"ship it"}`), store.Status{PR: "https://github.com/o/r/pull/1"})
	if s.PR != "" {
		t.Errorf("new turn kept old PR: %q", s.PR)
	}
	s = c.MapHook("PostToolUse", []byte(`{"tool_name":"Bash","tool_response":{"stdout":"remote: Create a pull request by visiting: https://github.com/o/r/pull/new/ctabs/x"}}`), s)
	if s.PR != "" {
		t.Errorf("push hint taken as a PR: %q", s.PR)
	}
	s = c.MapHook("PostToolUse", []byte(`{"tool_name":"Bash","tool_response":{"stdout":"https://github.com/my-org/my.repo/pull/42\n"}}`), s)
	s = c.MapHook("PostToolUse", []byte(`{"tool_name":"Read","tool_response":{}}`), s)
	s = c.MapHook("Stop", []byte(`{}`), s)
	if s.PR != "https://github.com/my-org/my.repo/pull/42" {
		t.Errorf("PR = %q", s.PR)
	}
}

func TestClaudeCommand(t *testing.T) {
	c := claude{}
	fresh := c.Command(LaunchOpts{SessionID: "u1", Model: "opus", SettingsPath: "/h.json", Prompt: "hi"})
	want := []string{"claude", "--model", "opus", "--settings", "/h.json", "--session-id", "u1", "--", "hi"}
	if !slices.Equal(fresh, want) {
		t.Errorf("fresh: %q", fresh)
	}
	resume := c.Command(LaunchOpts{SessionID: "u1", Resume: true, Prompt: "ignored"})
	if !slices.Equal(resume, []string{"claude", "--resume", "u1"}) {
		t.Errorf("resume: %q", resume)
	}
}

func TestClaudeHookSettings(t *testing.T) {
	b, err := claude{}.HookSettings(func(ev string) string { return "ctabs hook X " + ev })
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Hooks map[string][]struct {
			Matcher string
			Hooks   []struct{ Type, Command string }
		}
	}
	if err := json.Unmarshal(b, &parsed); err != nil {
		t.Fatal(err)
	}
	pre := parsed.Hooks["PreToolUse"]
	if len(pre) != 1 || pre[0].Matcher != "*" || pre[0].Hooks[0].Command != "ctabs hook X PreToolUse" {
		t.Errorf("PreToolUse: %+v", pre)
	}
	if len(parsed.Hooks) != len(hookEvents) {
		t.Errorf("got %d events", len(parsed.Hooks))
	}
}

func TestClaudePromptAndFinished(t *testing.T) {
	c := claude{}
	s := c.MapHook("UserPromptSubmit", []byte(`{"prompt":"fix the\n  login bug"}`), store.Status{State: store.StateReady})
	if s.Prompt != "fix the login bug" {
		t.Fatalf("prompt %q", s.Prompt)
	}
	s = c.MapHook("PreToolUse", []byte(`{"tool_name":"Bash"}`), s)
	s = c.MapHook("Stop", []byte(`{}`), s)
	if s.Prompt != "fix the login bug" || s.Finished.IsZero() || s.Tool != "" {
		t.Fatalf("after stop %+v", s)
	}
	if !s.Unseen(s.Finished.Add(-time.Second)) || s.Unseen(s.Finished.Add(time.Second)) {
		t.Fatalf("unseen logic wrong")
	}
}

func TestClaudeInspect(t *testing.T) {
	c := claude{}
	busy := "❯ Run the shell command\n✶ Kerfuffling… (4s · ↓ 257 tokens · thought for 2s)\n────\n❯ \n────\n"
	menu := " Do you want to make this edit to calc.py?\n ❯ 1. Yes\n   2. Yes, and switch\n   3. No\n"
	idle := "⏺ Done.\n────\n❯ \n────\n  ⏸ manual mode on\n"
	trust := " Quick safety check: Is this a project you trust?\n Yes, I trust this folder\n"
	for screen, want := range map[string]Screen{busy: ScreenBusy, menu: ScreenWaiting, idle: ScreenIdle, trust: ScreenUnknown} {
		if got := c.Inspect(screen); got != want {
			t.Errorf("Inspect(%q) = %v, want %v", screen, got, want)
		}
	}
}
