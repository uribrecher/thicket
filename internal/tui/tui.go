// Package tui hides the interactive selection / confirmation flow behind a
// small Selector interface so the CLI's `start` command can compose it
// with the same shape whether running interactively (bubbletea) or in
// `--no-interactive` mode.
package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/huh"

	"github.com/uribrecher/thicket/internal/catalog"
	"github.com/uribrecher/thicket/internal/detector"
)

// Selector drives the user-facing selection + confirm prompts.
type Selector interface {
	// SelectRepos asks the user which repos to include in the workspace.
	// `picks` is the LLM's pre-selection. Implementations are free to
	// pre-load them or ignore them. Returns the chosen repo names in
	// catalog order. Returns ErrCancelled if the user aborted.
	SelectRepos(catalog []catalog.Repo, picks []detector.RepoMatch) ([]string, error)

	// ConfirmClone asks the user whether to clone a repo that isn't yet
	// available locally. Returns false on a "no" answer.
	ConfirmClone(repoName, targetPath string) (bool, error)
}

// ErrCancelled signals the user pressed Esc / Ctrl-C — the start flow
// should exit without making changes.
var ErrCancelled = huh.ErrUserAborted

// ----- bubbletea-backed implementation -----

// HuhSelector keeps the name for backward compatibility; internally the
// repo picker now runs as a bubbletea program (see picker.go). The
// clone-confirm prompt still uses huh.NewConfirm — it's a one-shot Y/n
// and doesn't justify a custom view.
type HuhSelector struct{}

// SelectRepos delegates to the bubbletea repo picker. LLM picks come
// pre-loaded into the selection with their reasons rendered next to
// each entry; live fuzzy search drives the match list as the user types.
func (HuhSelector) SelectRepos(cat []catalog.Repo, picks []detector.RepoMatch) ([]string, error) {
	if len(picks) > 0 {
		fmt.Printf("\nLLM suggested %d repo(s): %s\n", len(picks), summarizePickNames(picks))
	}
	return runPicker(cat, picks)
}

func summarizePickNames(picks []detector.RepoMatch) string {
	names := make([]string, 0, len(picks))
	for _, p := range picks {
		names = append(names, p.Name)
	}
	return strings.Join(names, ", ")
}

func removeFromSlice(s []string, v string) []string {
	out := s[:0]
	for _, x := range s {
		if x != v {
			out = append(out, x)
		}
	}
	return out
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

func Truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n < 1 {
		return ""
	}
	return string(r[:n-1]) + "…"
}

func PadRight(s string, n int) string {
	r := []rune(s)
	if len(r) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-len(r))
}

// ----- non-interactive selector -----

// AutoSelector accepts the LLM picks unchanged and answers "yes" to every
// clone prompt. Used when the CLI is run with --no-interactive.
type AutoSelector struct {
	AutoClone bool
}

// SelectRepos filters the LLM picks against the actual catalog
// (skipping hallucinations), dedupes them, and returns the survivors
// in catalog order — matching the interface docstring.
func (a AutoSelector) SelectRepos(cat []catalog.Repo, picks []detector.RepoMatch) ([]string, error) {
	keep := make(map[string]bool, len(picks))
	inCat := make(map[string]bool, len(cat))
	for _, r := range cat {
		inCat[r.Name] = true
	}
	for _, p := range picks {
		if inCat[p.Name] {
			keep[p.Name] = true
		}
	}
	out := make([]string, 0, len(keep))
	for _, r := range cat {
		if keep[r.Name] {
			out = append(out, r.Name)
		}
	}
	return out, nil
}

func (a AutoSelector) ConfirmClone(string, string) (bool, error) { return a.AutoClone, nil }
