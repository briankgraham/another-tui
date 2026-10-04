package worktree

import (
	"os"
	"path/filepath"
	"testing"
)

func newRepo(t *testing.T) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "myrepo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "t"},
	} {
		if _, err := git(repo, args...); err != nil {
			t.Fatal(err)
		}
	}
	write(t, filepath.Join(repo, "a.txt"), "one\ntwo\n")
	if _, err := git(repo, "add", "."); err != nil {
		t.Fatal(err)
	}
	if _, err := git(repo, "commit", "-qm", "init"); err != nil {
		t.Fatal(err)
	}
	return repo
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCreateDiffRemove(t *testing.T) {
	repo := newRepo(t)
	root := t.TempDir()

	c, err := Create(repo, root, "fix-bug", "")
	if err != nil {
		t.Fatal(err)
	}
	if c.Branch != "ctabs/fix-bug" || c.Path != filepath.Join(root, "myrepo", "fix-bug") {
		t.Fatalf("got %+v", c)
	}
	if got, err := RepoRoot(c.Path); err != nil || got != mustEval(t, repo) {
		t.Fatalf("RepoRoot from worktree = %q, %v", got, err)
	}

	c2, err := Create(repo, root, "fix-bug", "main")
	if err != nil {
		t.Fatal(err)
	}
	if c2.Branch != "ctabs/fix-bug-2" {
		t.Fatalf("collision not avoided: %+v", c2)
	}

	write(t, filepath.Join(c.Path, "a.txt"), "one\nTWO\nthree\n")
	write(t, filepath.Join(c.Path, "new.txt"), "x\n")
	st, err := Diff(c.Path, c.BaseCommit)
	if err != nil {
		t.Fatal(err)
	}
	if st.Files != 2 || st.Added != 2 || st.Deleted != 1 || st.Dirty != 2 || st.Commits != 0 {
		t.Fatalf("stats %+v", st)
	}

	if err := Remove(repo, c.Path, c.Branch, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(c.Path); !os.IsNotExist(err) {
		t.Fatalf("worktree still exists")
	}
	if _, err := git(repo, "rev-parse", "--verify", "-q", "refs/heads/ctabs/fix-bug"); err == nil {
		t.Fatalf("branch not deleted")
	}
}

func TestBranchCommits(t *testing.T) {
	repo := newRepo(t)
	c, err := Create(repo, t.TempDir(), "work", "")
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(c.Path, "b.txt"), "b\n")
	for _, args := range [][]string{{"add", "."}, {"commit", "-qm", "b"}, {"checkout", "-q", "-b", "elsewhere"}} {
		if _, err := git(c.Path, args...); err != nil {
			t.Fatal(err)
		}
	}
	// The agent moved the worktree to another branch, then the folder vanished:
	// the commit on the session branch must still be counted.
	if err := os.RemoveAll(c.Path); err != nil {
		t.Fatal(err)
	}
	if n, err := BranchCommits(repo, c.BaseCommit, c.Branch); err != nil || n != 1 {
		t.Fatalf("BranchCommits = %d, %v; want 1", n, err)
	}
	if _, err := Diff(c.Path, c.BaseCommit); err == nil {
		t.Fatalf("Diff of a missing worktree should fail")
	}
	if _, err := BranchCommits(repo, c.BaseCommit, "ctabs/nope"); err == nil {
		t.Fatalf("missing branch should fail")
	}
}

func mustEval(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestSlug(t *testing.T) {
	for in, want := range map[string]string{
		"Fix the Bug!": "fix-the-bug",
		"  ":           "session",
		"a/b c":        "a-b-c",
	} {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}
