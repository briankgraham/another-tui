// Package worktree manages the git worktree backing each session.
package worktree

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

func git(dir string, args ...string) (string, error) {
	// --no-optional-locks: status/diff refresh the index, and taking index.lock
	// here would make the agent's own `git commit` fail at random.
	cmd := exec.Command("git", append([]string{"--no-optional-locks", "-C", dir}, args...)...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return strings.TrimRight(out.String(), "\n"), nil
}

// RepoRoot returns the top-level directory of the main repository containing dir,
// even when dir is itself inside a linked worktree.
func RepoRoot(dir string) (string, error) {
	common, err := git(dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	if filepath.Base(common) == ".git" {
		return filepath.Dir(common), nil
	}
	return git(dir, "rev-parse", "--show-toplevel")
}

// CurrentBranch returns the branch checked out in repo, or the short SHA when detached.
func CurrentBranch(repo string) (string, error) {
	if b, err := git(repo, "symbolic-ref", "--short", "-q", "HEAD"); err == nil && b != "" {
		return b, nil
	}
	return git(repo, "rev-parse", "--short", "HEAD")
}

var unsafe = regexp.MustCompile(`[^a-z0-9._-]+`)

// Slug turns a display name into something safe for branch and directory names.
func Slug(name string) string {
	s := unsafe.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "-")
	s = strings.Trim(s, "-.")
	if s == "" {
		s = "session"
	}
	return s
}

type Created struct {
	Path       string
	Branch     string
	BaseCommit string
}

// Create adds a worktree for slug under root/<repo-name>/<slug> on a new branch
// ctabs/<slug> starting at base. A numeric suffix avoids collisions.
func Create(repo, root, slug, base string) (Created, error) {
	if base == "" {
		base = "HEAD"
	}
	commit, err := git(repo, "rev-parse", "--verify", base+"^{commit}")
	if err != nil {
		return Created{}, fmt.Errorf("base %q: %w", base, err)
	}
	parent := filepath.Join(root, filepath.Base(repo))
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return Created{}, err
	}
	for i := 1; i < 100; i++ {
		name := slug
		if i > 1 {
			name = fmt.Sprintf("%s-%d", slug, i)
		}
		path := filepath.Join(parent, name)
		branch := "ctabs/" + name
		if _, err := os.Stat(path); err == nil {
			continue
		}
		if _, err := git(repo, "rev-parse", "--verify", "-q", "refs/heads/"+branch); err == nil {
			continue
		}
		if _, err := git(repo, "worktree", "add", "-b", branch, path, commit); err != nil {
			return Created{}, err
		}
		return Created{Path: path, Branch: branch, BaseCommit: commit}, nil
	}
	return Created{}, fmt.Errorf("no free worktree name for %q", slug)
}

// Remove deletes the worktree (discarding local changes) and optionally its branch.
func Remove(repo, path, branch string, deleteBranch bool) error {
	if _, err := os.Stat(path); err == nil {
		if _, err := git(repo, "worktree", "remove", "--force", path); err != nil {
			return err
		}
		_ = os.Remove(filepath.Dir(path)) // the per-repo folder, once empty
	} else {
		_, _ = git(repo, "worktree", "prune")
	}
	if deleteBranch && branch != "" {
		if _, err := git(repo, "branch", "-D", branch); err != nil {
			return err
		}
	}
	return nil
}

// BranchCommits counts commits on branch that are not in baseCommit. It asks the
// main repo, so it still works when the worktree folder is gone or the agent
// checked out a different branch there.
func BranchCommits(repo, baseCommit, branch string) (int, error) {
	if baseCommit == "" || branch == "" {
		return 0, fmt.Errorf("unknown base or branch")
	}
	n, err := git(repo, "rev-list", "--count", baseCommit+"..refs/heads/"+branch)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(n)
}

// Stats summarizes how far a worktree has moved from its base commit,
// counting both committed and uncommitted work.
type Stats struct {
	Files   int // tracked files changed + untracked files
	Added   int
	Deleted int
	Dirty   int // uncommitted entries
	Commits int // commits on top of base
}

var shortstat = regexp.MustCompile(`(\d+) (file|insertion|deletion)`)

func Diff(path, baseCommit string) (Stats, error) {
	var st Stats
	if baseCommit == "" {
		baseCommit = "HEAD"
	}
	out, err := git(path, "diff", "--shortstat", baseCommit)
	if err != nil {
		return st, err
	}
	for _, m := range shortstat.FindAllStringSubmatch(out, -1) {
		n, _ := strconv.Atoi(m[1])
		switch m[2] {
		case "file":
			st.Files = n
		case "insertion":
			st.Added = n
		case "deletion":
			st.Deleted = n
		}
	}
	untracked, err := git(path, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return st, err
	}
	st.Files += countLines(untracked)

	porcelain, err := git(path, "status", "--porcelain")
	if err != nil {
		return st, err
	}
	st.Dirty = countLines(porcelain)

	if n, err := git(path, "rev-list", "--count", baseCommit+"..HEAD"); err == nil {
		st.Commits, _ = strconv.Atoi(n)
	}
	return st, nil
}

// Ignored counts untracked-but-ignored entries (.env, build output, ...). Diff
// leaves them out, but Remove deletes them. Directories count once.
func Ignored(path string) (int, error) {
	out, err := git(path, "ls-files", "--others", "--ignored", "--exclude-standard", "--directory")
	if err != nil {
		return 0, err
	}
	return countLines(out), nil
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}
