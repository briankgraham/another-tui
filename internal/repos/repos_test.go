package repos

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func mkrepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDiscover(t *testing.T) {
	root := t.TempDir()
	mkrepo(t, filepath.Join(root, "a"))
	mkrepo(t, filepath.Join(root, "code", "b"))
	mkrepo(t, filepath.Join(root, "a", "nested"))         // inside a repo: skipped
	mkrepo(t, filepath.Join(root, ".hidden", "c"))        // hidden: skipped
	mkrepo(t, filepath.Join(root, "node_modules", "d"))   // skipped
	mkrepo(t, filepath.Join(root, "x", "y", "z", "deep")) // too deep for 3
	got := Discover(context.Background(), []string{root}, 3)
	want := []string{filepath.Join(root, "a"), filepath.Join(root, "code", "b")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	if b := Branch(filepath.Join(root, "a")); b != "main" {
		t.Fatalf("branch %q", b)
	}
}

func TestMatch(t *testing.T) {
	if _, _, ok := Match("xyz", "~/code/tui"); ok {
		t.Fatal("unexpected match")
	}
	s1, pos, ok := Match("tui", "~/Documents/tui-for-multiple-ais")
	if !ok || len(pos) != 3 {
		t.Fatalf("no match: %v", pos)
	}
	s2, _, _ := Match("tui", "~/Documents/test/utils/install")
	if s1 <= s2 {
		t.Fatalf("contiguous basename match should win: %d <= %d", s1, s2)
	}
}

func TestRecent(t *testing.T) {
	t.Setenv("CTABS_HOME", t.TempDir())
	a, b := t.TempDir(), t.TempDir()
	mkrepo(t, a)
	mkrepo(t, b)
	_ = Touch(a)
	_ = Touch(b)
	_ = Touch(a)
	if got := Recent(); !reflect.DeepEqual(got, []string{a, b}) {
		t.Fatalf("got %v", got)
	}
}
