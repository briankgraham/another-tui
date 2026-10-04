// Package repos finds git repositories to start sessions in: recently used
// ones, ones discovered on disk, and fuzzy matching over their paths.
package repos

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"ctabs/internal/store"
)

const maxRecent = 30

func recentPath() string { return filepath.Join(store.Dir(), "recent.json") }
func cachePath() string  { return filepath.Join(store.Dir(), "repos-cache.json") }

func loadList(path string) []string {
	var l []string
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &l)
	}
	return l
}

func saveList(path string, l []string) error {
	b, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	return store.WriteAtomic(path, b)
}

// Recent returns recently used repositories, most recent first, skipping any
// that no longer exist.
func Recent() []string {
	var out []string
	for _, p := range loadList(recentPath()) {
		if IsRepo(p) {
			out = append(out, p)
		}
	}
	return out
}

// Touch moves repo to the front of the recent list.
func Touch(repo string) error {
	l := []string{repo}
	for _, p := range loadList(recentPath()) {
		if p != repo && len(l) < maxRecent {
			l = append(l, p)
		}
	}
	return saveList(recentPath(), l)
}

// Cached returns the repositories found by the last scan.
func Cached() []string { return loadList(cachePath()) }

// SaveCache stores the result of a scan for instant display next time.
func SaveCache(l []string) error { return saveList(cachePath(), l) }

// IsRepo reports whether dir is the top of a git repository (not a worktree or submodule).
func IsRepo(dir string) bool {
	fi, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil && fi.IsDir()
}

// Branch reads the checked-out branch from .git/HEAD without running git.
// It returns a short commit for a detached HEAD and "" if unknown.
func Branch(repo string) string {
	b, err := os.ReadFile(filepath.Join(repo, ".git", "HEAD"))
	if err != nil {
		return ""
	}
	head := strings.TrimSpace(string(b))
	if ref, ok := strings.CutPrefix(head, "ref: refs/heads/"); ok {
		return ref
	}
	if len(head) >= 7 {
		return head[:7]
	}
	return ""
}

// skipDirs are never worth descending into when looking for repositories.
var skipDirs = map[string]bool{
	"node_modules": true, "vendor": true, "Library": true, "Applications": true,
	"Pictures": true, "Music": true, "Movies": true, "Public": true,
	"target": true, "dist": true, "build": true, "venv": true, "__pycache__": true,
	"pkg": true, // ~/go/pkg module cache
}

// Discover walks roots breadth first, up to maxDepth levels deep, and returns
// every repository it finds. It does not look inside repositories, hidden
// directories or symlinks, and stops early when ctx is done.
func Discover(ctx context.Context, roots []string, maxDepth int) []string {
	type item struct {
		dir   string
		depth int
	}
	seen := map[string]bool{}
	var found []string
	var queue []item
	for _, r := range roots {
		queue = append(queue, item{r, 0})
	}
	for len(queue) > 0 && ctx.Err() == nil {
		it := queue[0]
		queue = queue[1:]
		if seen[it.dir] {
			continue
		}
		seen[it.dir] = true
		if IsRepo(it.dir) {
			found = append(found, it.dir)
			continue
		}
		if it.depth >= maxDepth {
			continue
		}
		entries, err := os.ReadDir(it.dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			n := e.Name()
			if !e.IsDir() || strings.HasPrefix(n, ".") || skipDirs[n] {
				continue
			}
			queue = append(queue, item{filepath.Join(it.dir, n), it.depth + 1})
		}
	}
	sort.Slice(found, func(i, j int) bool {
		return strings.ToLower(filepath.Base(found[i])) < strings.ToLower(filepath.Base(found[j]))
	})
	return found
}

// Tilde shortens a path under the home directory to ~/...
func Tilde(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if rest, ok := strings.CutPrefix(p, home+string(filepath.Separator)); ok {
		return "~/" + rest
	}
	return p
}

// Expand turns a leading ~ into the home directory.
func Expand(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return home + p[1:]
		}
	}
	return p
}

// Match fuzzy-matches query against target, case-insensitively, as a
// subsequence. It returns a score (higher is better) and the matched rune
// positions in target. Matches at word starts, runs of consecutive characters
// and matches in the last path element score higher.
func Match(query, target string) (int, []int, bool) {
	q := []rune(strings.ToLower(strings.ReplaceAll(query, " ", "")))
	if len(q) == 0 {
		return 0, nil, true
	}
	t := []rune(target)
	lower := []rune(strings.ToLower(target))
	base := strings.LastIndex(target, "/") + 1
	baseRune := len([]rune(target[:base]))

	pos := make([]int, 0, len(q))
	score, qi, prev := 0, 0, -2
	for i := 0; i < len(lower) && qi < len(q); i++ {
		if lower[i] != q[qi] {
			continue
		}
		s := 1
		if i == 0 || !unicode.IsLetter(t[i-1]) && !unicode.IsDigit(t[i-1]) {
			s += 8 // word start
		} else if unicode.IsUpper(t[i]) && unicode.IsLower(t[i-1]) {
			s += 6 // camelCase hump
		}
		if prev == i-1 {
			s += 5
		}
		if i >= baseRune {
			s += 3
		}
		score += s
		pos = append(pos, i)
		prev = i
		qi++
	}
	if qi < len(q) {
		return 0, nil, false
	}
	score -= len(t) / 10 // prefer shorter paths on ties
	return score, pos, true
}
