# TODO

Ideas for ctabs, roughly in priority order.

## Next
- [ ] **Worktree setup hook**: on create, copy `.env*` files and run an install command (configurable per repo, via `~/.ctabs/config.json` or a committed `.ctabs.json`). Run it in the session's pane before `claude` starts so it doesn't block the sidebar.
- [ ] **Popup helper** in `internal/tmux` (`display-popup`), shared by the next two items.
  - [ ] **Shell in the worktree**: `^]` menu item opens `$SHELL` in a popup.
  - [ ] **Diff viewer**: `^]` → `v` shows `git diff <base>...` in a scrollable popup (`delta` if installed, else `less -R`).
- [ ] **Open in editor**: open the worktree in `$EDITOR` / `code` / `cursor`.
- [ ] **Open a PR**: push the branch and run `gh pr create`.
- [ ] **Warn on close** if the worktree has uncommitted or unpushed changes.

## Review findings (2026-10-04)
From a full code review. Roughly by severity.

- [ ] **Diff polling piles up** (`sidebar.go` diffTicker): re-arm only after `diffMsg` arrives; drop out-of-order `diffMsg`/`refreshMsg`.
- [ ] **Status read-modify-write isn't locked** (`store.UpdateStatusIf`, `hook` in main.go): a hook and the reconciler, or two hooks, can lose each other's update. Add a per-id flock.
- [ ] **A session can become impossible to close** (`worktree.Remove`, `CloseSession`): if the repo was moved or the branch is already gone, it errors forever after killing the pane. Treat "branch not found" and a missing repo as success, and always drop the state entry once the pane is gone.
- [ ] **A timed-out repo scan overwrites the full cache** (`repopicker.go`): only save when the scan finished, or merge.
- [ ] **Typing `~/` key by key jumps to `/`** in the repo picker (pasting works).
- [ ] **Two panes for one session** (`EnsurePane`): check-then-create isn't locked, so a double Enter or M-1 twice starts two `claude --resume`. Hold a per-session lock.
- [ ] Notifications that aren't permission requests (e.g. `auth_success`) mark the session "needs you". Allow-list `permission_prompt` and `elicitation_dialog`.
- [ ] A `SessionEnd` hook firing during close recreates `status/<id>.json`, which leaks. `seen/<id>` is never removed on close.
- [ ] `Bootstrap` repairs a dead sidebar pane but not a missing one.
- [ ] `CTABS_HOME` isn't made absolute or checked for ownership. The README's `/tmp/ctabs-dev` could be pre-created by another user who plants hook commands.
- [ ] Menu commands quote the exe for the shell but not for tmux (`"`, `\`, `$` in the install path).
- [ ] Picker scroll is off by one (`listHeight()` is `h-7` but the layout uses 8 lines).
- [ ] Picker `pgup` does nothing when it lands on the "Recent" header.
- [ ] Clicking the sidebar footer selects an off-screen session, and clicks also work while help is open.
- [ ] Browse mode does `ReadDir` and a stat per entry on the UI thread on every keystroke; `selectRow` runs `tmux.Show` on the UI thread.
- [ ] `WriteAtomic` doesn't fsync before rename, so state.json can come back empty after a power loss.
- [ ] `Slug` allows names that aren't valid branch names (`v1..2`, `foo.lock`); concurrent creates with the same slug don't retry with the next suffix.
- [ ] Put `--end-of-options` before user-supplied revisions (Base) in git calls.
- [ ] Worktree tests inherit global git config (e.g. `commit.gpgsign`).

## Later
- [ ] **Rebase onto latest base**: only when the session is idle/done; on conflicts, hand them to Claude as a prompt.
- [ ] **Follow-up composer**: send a message to a session from the menu without switching to it (`send-keys`, only when idle/done).
- [ ] **Cost / tokens per tab**, read from Claude's transcript files (`~/.claude/projects/<encoded-path>/<session>.jsonl`).
- [ ] **Start from an existing branch or PR** instead of always creating a new branch. Handle branches already checked out in another worktree.
- [ ] **Worktree cleanup**: `ctabs gc` for orphaned worktrees under `~/.ctabs/worktrees` that aren't in `state.json`; show disk usage.
- [ ] **Branch name collisions**: handle `ctabs/<name>` already existing from a previous session.
- [ ] Other agents (Codex / OpenAI) behind the `agent.Agent` interface.
- [ ] **Side-by-side view** of two sessions (conflicts with the swap-in model).
- [ ] ~~Merge into base locally~~: probably skip. Base is usually checked out in the main repo, so this is only safe as a fast-forward ref update. Prefer the PR flow.

## Done
- [x] Tabs sidebar with live status, auto worktrees, model picker
- [x] Unseen "done" state, jump to attention, sounds + desktop notifications, last prompt on each tab, cancelled-turn detection
- [x] `^]` leader menu from anywhere (new, switch, detach, quit)
- [x] Repository picker: recent repos, repos found on disk, folder browser
- [x] Close popup defaults to keeping the branch when it can't inspect the worktree or count the branch's commits (counted from the main repo, so it works when the worktree is gone)
- [x] Resume follows `/clear` and `/resume` (current conversation id comes from hooks); `/clear` no longer shows the session as exited
- [x] Restore after reboot clears all old pane ids up front and keeps going past a session that fails
- [x] Review fixes: close warns about ignored files (`.env`) and re-checks after the pane is killed; `configure` tolerates old tmux and a bad leader key (and tears down a half-started server); the first prompt follows `--`; git calls use `--no-optional-locks`
