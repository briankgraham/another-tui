package ui

import (
	"bufio"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/huh"

	"ctabs/internal/agent"
	"ctabs/internal/app"
	"ctabs/internal/repos"
	"ctabs/internal/store"
	"ctabs/internal/tmux"
	"ctabs/internal/worktree"
)

var (
	adjectives = []string{"amber", "brisk", "calm", "clever", "cosmic", "daring", "eager", "fuzzy", "gentle", "keen", "lucky", "mellow", "nimble", "quiet", "rapid", "sly", "steady", "swift", "tidy", "witty"}
	nouns      = []string{"otter", "falcon", "badger", "heron", "lynx", "marten", "osprey", "panda", "quokka", "raven", "seal", "tapir", "walrus", "wren", "yak", "koala", "gecko", "ibis", "moose", "newt"}
)

func randomName() string {
	return adjectives[rand.IntN(len(adjectives))] + "-" + nouns[rand.IntN(len(nouns))]
}

// fail shows err inside the popup and waits so it can be read before the popup closes.
func fail(err error) error {
	fmt.Fprintln(os.Stderr, "\n  "+sDel.Render("✕ "+err.Error()))
	fmt.Fprint(os.Stderr, "\n  "+sDim.Render("press enter to close"))
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	return err
}

func aborted(err error) bool { return errors.Is(err, huh.ErrUserAborted) }

// newForm builds a popup form that esc also dismisses.
func newForm(fields ...huh.Field) *huh.Form {
	km := huh.NewDefaultKeyMap()
	km.Quit = key.NewBinding(key.WithKeys("esc", "ctrl+c"))
	return huh.NewForm(huh.NewGroup(fields...)).WithTheme(huh.ThemeCharm()).WithKeyMap(km)
}

// RunNew is the "new session" popup.
func RunNew() error {
	a, err := agent.Get("claude")
	if err != nil {
		return fail(err)
	}
	cfg := app.LoadConfig(a)

	initial := tmux.GetOption(tmux.OptRepo)
	if initial == "" {
		if wd, err := os.Getwd(); err == nil {
			initial, _ = worktree.RepoRoot(wd)
		}
	}
	repo, err := PickRepo(initial, cfg.RepoRoots)
	if err != nil {
		return fail(err)
	}
	if repo == "" {
		return nil
	}
	branchHint := "current branch"
	if b, err := worktree.CurrentBranch(repo); err == nil {
		branchHint = b
	}

	name, model, prompt, base := randomName(), cfg.Models[0].ID, "", ""
	modelOpts := make([]huh.Option[string], len(cfg.Models))
	for i, m := range cfg.Models {
		modelOpts[i] = huh.NewOption(m.Label, m.ID)
	}

	form := newForm(
		huh.NewNote().Title("New session").
			Description("in "+sName.Render(repos.Tilde(repo))+sDim.Render(" · "+branchHint)),
		huh.NewInput().Title("Name").Value(&name).
			Validate(func(s string) error {
				if strings.TrimSpace(s) == "" {
					return errors.New("name is required")
				}
				return nil
			}),
		huh.NewSelect[string]().Title("Model").Options(modelOpts...).Height(len(modelOpts)+1).Value(&model),
		huh.NewText().Title("First prompt").Description("optional · alt+enter for newline").
			Lines(3).Value(&prompt),
		huh.NewInput().Title("Base").Placeholder(branchHint).Value(&base),
	)

	if err := form.Run(); err != nil {
		if aborted(err) {
			return nil
		}
		return fail(err)
	}
	fmt.Println("\n  " + sDim.Render("Creating worktree and starting Claude…"))
	if _, err := app.CreateSession(app.NewOpts{
		Name:   strings.TrimSpace(name),
		Repo:   repo,
		Base:   strings.TrimSpace(base),
		Model:  model,
		Prompt: strings.TrimSpace(prompt),
	}); err != nil {
		return fail(err)
	}
	return nil
}

// RunClose is the "close session" popup; with no id it targets the staged session.
func RunClose(id string) error {
	st, err := store.Load()
	if err != nil {
		return fail(err)
	}
	var s *store.Session
	if id == "" {
		if staged, ok := app.StagedSession(); ok {
			_, s = st.Find(staged.ID)
		}
	} else {
		_, s = st.Find(id)
	}
	if s == nil {
		return fail(errors.New("no session selected"))
	}

	// Deleting the branch is only the default when we know there is nothing to
	// lose; if either check fails, assume there is.
	d, diffErr := worktree.Diff(s.Worktree, s.BaseCommit)
	commits, commitsErr := worktree.BranchCommits(s.Repo, s.BaseCommit, s.Branch)
	var desc []string
	desc = append(desc, sDim.Render(s.Branch+" · "+s.Worktree))
	if diffErr != nil {
		desc = append(desc, sNeeds.Render("⚠ couldn't check for uncommitted changes: "+diffErr.Error()))
	} else if d.Dirty > 0 {
		desc = append(desc, sNeeds.Render(fmt.Sprintf("⚠ %d uncommitted change(s) will be discarded", d.Dirty)))
	}
	if commitsErr != nil {
		desc = append(desc, sNeeds.Render("⚠ couldn't count commits on the branch: "+commitsErr.Error()))
	} else if commits > 0 {
		desc = append(desc, sText.Render(fmt.Sprintf("%d commit(s) on the branch", commits)))
	}

	keep := huh.NewOption("Remove worktree, keep branch", "keep")
	del := huh.NewOption("Remove worktree and delete branch", "delete")
	cancel := huh.NewOption("Cancel", "cancel")
	opts := []huh.Option[string]{del, keep, cancel}
	if diffErr != nil || commitsErr != nil || commits > 0 || d.Dirty > 0 {
		opts = []huh.Option[string]{keep, del, cancel}
	}
	choice := opts[0].Value
	if err := newForm(
		huh.NewSelect[string]().Title("Close “" + s.Name + "”?").
			Description(strings.Join(desc, "\n")).Options(opts...).Value(&choice),
	).Run(); err != nil || choice == "cancel" {
		if err != nil && !aborted(err) {
			return fail(err)
		}
		return nil
	}
	if err := app.CloseSession(s.ID, choice == "delete"); err != nil {
		return fail(err)
	}
	return nil
}

// RunRename is the "rename session" popup.
func RunRename(id string) error {
	st, err := store.Load()
	if err != nil {
		return fail(err)
	}
	_, s := st.Find(id)
	if s == nil {
		return fail(fmt.Errorf("no session %s", id))
	}
	name := s.Name
	if err := newForm(
		huh.NewInput().Title("Rename session").Value(&name),
	).Run(); err != nil {
		if aborted(err) {
			return nil
		}
		return fail(err)
	}
	if name = strings.TrimSpace(name); name == "" || name == s.Name {
		return nil
	}
	if err := app.Rename(id, name); err != nil {
		return fail(err)
	}
	return nil
}
