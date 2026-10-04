package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"ctabs/internal/store"
	"ctabs/internal/worktree"
)

func TestCloseSeenStale(t *testing.T) {
	repo := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", "init"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	c, err := worktree.Create(repo, t.TempDir(), "x", "")
	if err != nil {
		t.Fatal(err)
	}
	sess := store.Session{Repo: repo, Worktree: c.Path, Branch: c.Branch, BaseCommit: c.BaseCommit}
	seen := &CloseSeen{}

	if why, err := seen.stale(sess, true); err != nil || why != "" {
		t.Fatalf("clean: %q, %v", why, err)
	}
	if err := os.WriteFile(filepath.Join(c.Path, "new.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if why, err := seen.stale(sess, true); err != nil || !strings.Contains(why, "uncommitted") {
		t.Fatalf("dirty: %q, %v", why, err)
	}
	if why, _ := (&CloseSeen{Dirty: 1}).stale(sess, true); why != "" {
		t.Fatalf("unchanged work flagged: %q", why)
	}
}
