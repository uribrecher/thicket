// Package tui hides the interactive selection / confirmation flow behind a
// small Selector interface so the CLI's `start` command can compose it
// with the same shape whether running interactively (huh) or in
// `--no-interactive` mode.
package tui

import (
	"fmt"
	"sort"

	"github.com/charmbracelet/huh"

	"github.com/uribrecher/thicket/internal/catalog"
	"github.com/uribrecher/thicket/internal/detector"
)

// Selector drives the user-facing selection + confirm prompts.
type Selector interface {
	// SelectRepos shows a multi-select over the full catalog with the LLM's
	// picks pre-checked. Returns the names of the repos the user kept,
	// in input order (catalog order). Returns ErrCancelled if the user
	// aborted.
	SelectRepos(catalog []catalog.Repo, picks []detector.RepoMatch) ([]string, error)

	// ConfirmClone asks the user whether to clone a repo that isn't yet
	// available locally. Returns false on a "no" answer.
	ConfirmClone(repoName, targetPath string) (bool, error)
}

// ErrCancelled signals the user pressed Esc / Ctrl-C — the start flow
// should exit without making changes.
var ErrCancelled = huh.ErrUserAborted

// ----- huh-backed implementation -----

type HuhSelector struct{}

func (HuhSelector) SelectRepos(cat []catalog.Repo, picks []detector.RepoMatch) ([]string, error) {
	pickMap := make(map[string]detector.RepoMatch, len(picks))
	for _, p := range picks {
		pickMap[p.Name] = p
	}

	type entry struct {
		name string
		isLLMPick bool
	}
	// Sort: LLM picks first (by descending confidence), then the rest of
	// the catalog alphabetically. This puts the suggested repos at the top
	// for easy review without hiding the long-tail catalog.
	entries := make([]entry, 0, len(cat))
	for _, r := range cat {
		_, isPick := pickMap[r.Name]
		entries = append(entries, entry{name: r.Name, isLLMPick: isPick})
	}
	sort.SliceStable(entries, func(i, j int) bool {
		ip, jp := entries[i].isLLMPick, entries[j].isLLMPick
		if ip != jp {
			return ip // picks first
		}
		if ip && jp {
			return pickMap[entries[i].name].Confidence > pickMap[entries[j].name].Confidence
		}
		return entries[i].name < entries[j].name
	})

	descByName := make(map[string]string, len(cat))
	for _, r := range cat {
		descByName[r.Name] = r.Description
	}

	opts := make([]huh.Option[string], 0, len(entries))
	for _, e := range entries {
		label := e.name
		if e.isLLMPick {
			m := pickMap[e.name]
			label = fmt.Sprintf("%s — %s (LLM %.0f%%: %s)", e.name,
				truncate(descByName[e.name], 48), m.Confidence*100, m.Reason)
		} else if d := descByName[e.name]; d != "" {
			label = fmt.Sprintf("%s — %s", e.name, truncate(d, 48))
		}
		opts = append(opts, huh.NewOption(label, e.name))
	}

	selected := make([]string, 0, len(picks))
	for _, p := range picks {
		selected = append(selected, p.Name)
	}

	form := huh.NewForm(huh.NewGroup(
		huh.NewMultiSelect[string]().
			Title("Select repos to include in the workspace").
			Description("LLM picks are pre-selected at the top. Space toggles. Type to filter.").
			Options(opts...).
			Value(&selected).
			Filterable(true).
			Height(15),
	))
	if err := form.Run(); err != nil {
		return nil, err
	}
	// Re-order result to follow catalog order for predictability downstream.
	keep := make(map[string]bool, len(selected))
	for _, s := range selected {
		keep[s] = true
	}
	out := make([]string, 0, len(selected))
	for _, r := range cat {
		if keep[r.Name] {
			out = append(out, r.Name)
		}
	}
	return out, nil
}

func (HuhSelector) ConfirmClone(repoName, targetPath string) (bool, error) {
	var ok bool
	err := huh.NewConfirm().
		Title(fmt.Sprintf("Clone %s?", repoName)).
		Description(fmt.Sprintf("Repo is not present at %s. Clone it now?", targetPath)).
		Affirmative("Yes, clone").
		Negative("Skip this repo").
		Value(&ok).
		Run()
	if err != nil {
		return false, err
	}
	return ok, nil
}

// ----- helpers -----

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n < 1 {
		return ""
	}
	return string(r[:n-1]) + "…"
}

// ----- non-interactive selector -----

// AutoSelector accepts the LLM picks unchanged and answers "yes" to every
// clone prompt. Used when the CLI is run with --no-interactive.
type AutoSelector struct {
	AutoClone bool
}

func (a AutoSelector) SelectRepos(_ []catalog.Repo, picks []detector.RepoMatch) ([]string, error) {
	out := make([]string, 0, len(picks))
	for _, p := range picks {
		out = append(out, p.Name)
	}
	return out, nil
}

func (a AutoSelector) ConfirmClone(string, string) (bool, error) { return a.AutoClone, nil }
