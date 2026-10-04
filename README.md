# ctabs

Run several Claude Code sessions side by side. Each session gets its own git worktree, and a sidebar of tabs shows what every session is doing.

```
┌─ ctabs ──────────────┬──────────────────────────────┐
│▌1 auth-refactor      │                              │
│▌ ⠋ Edit         Opus │   live `claude` session      │
│▌ +120 −34 · 3f    2m │                              │
│  2 flaky-test        │                              │
│  ◆ needs you  Sonnet │                              │
└──────────────────────┴──────────────────────────────┘
```

## Install & run

```sh
go install .          # or: go build -o ctabs .
cd your/repo && ctabs
```

You need `tmux` and the `claude` CLI on your PATH.

## Keys

**`Ctrl+]` from anywhere opens the ctabs menu.** It has new session, close or rename the current one, next needing you, jump to any session (`1`–`9`), focus sidebar, detach, and quit. It works in every terminal. You can change the key with `"leader"` in the config, e.g. `"C-\\"`.

In the sidebar: `j`/`k` preview, `enter` focus, `n` new, `.` next needing you, `r` rename, `x` close, `R` restart an exited session, `?` help. You can also click and scroll with the mouse.

Alt shortcuts (`M-n` new, `M-x` close, `M-j`/`M-k` switch, `M-1`…`M-9` jump, `M-.` next needing you, `M-s` sidebar, `M-q` detach) also work if your terminal sends Option as Meta. In iTerm2 that's Settings → Profiles → Keys → Left Option key → **Esc+**.

## Picking a repository

A new session starts by asking which repository to work in.

- **Search** (default) lists your recent repositories first, then every git repository found under your home folder. Type to fuzzy-match on the path (`saui` finds `~/SaaS-UI`).
- **Browse** (`tab`) walks folders like a file manager. Repositories show `⎇` and their branch. `enter` chooses a repository or opens a folder, `←` goes up, `→` opens, typing filters, and `/` opens the highlighted folder.
- Type or paste a path in either mode (`~/doc/tui`, `/srv/app`) to jump there. Each part is fuzzy-matched, so it doesn't need to be exact.


- **tmux:** ctabs runs a private tmux server (`tmux -L ctabs`), so it never touches your own tmux setup. The sidebar is a Bubble Tea app. Hidden sessions wait in a background tmux session and get swapped in when you select them.
- **Worktrees:** each session runs in `~/.ctabs/worktrees/<repo>/<name>` on branch `ctabs/<name>`, based on the branch you choose. Closing a session removes the worktree and optionally deletes the branch.
- **Status:** each `claude` is started with `--settings` hooks that call `ctabs hook`. Status files go to `~/.ctabs/status/`, and the sidebar watches them.
- **Resume:** sessions persist in `~/.ctabs/state.json`. After `ctabs kill` or a reboot, running `ctabs` brings every session back with `claude --resume`.
- **Trust:** Claude records folder trust against the main repo, so new worktrees don't ask for trust again.

## Config

Optional `~/.ctabs/config.json`. `repo_roots` sets where the repository picker looks for repositories (default: your home folder, 4 levels deep):

```json
{
  "leader": "C-]",
  "sounds": true,
  "notifications": true,
  "repo_roots": ["~/code", "~/Documents"],
  "models": [{ "id": "opus", "label": "Opus" }, { "id": "sonnet", "label": "Sonnet" }]
}
```

If a session opens a GitHub PR during a turn, the "finished" notification links to it. On macOS, clicking the banner opens the PR if `terminal-notifier` is installed (`brew install terminal-notifier`). Without it, a dialog with an "Open PR" button is shown instead.

## Developing

Set `CTABS_SOCKET` and `CTABS_HOME` to run a dev build next to your real instance without touching it:

```sh
go build -o ctabs . && CTABS_SOCKET=ctabs-dev CTABS_HOME=/tmp/ctabs-dev ./ctabs
```

## Adding other agents

Implement `agent.Agent` in `internal/agent/` (launch command, hook settings, and mapping hook events to status), then register it.
