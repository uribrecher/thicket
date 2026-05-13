// Package tui hides the interactive selection / confirmation flow behind a
// small Selector interface so the CLI's `start` command can compose it
// with the same shape whether running interactively (huh) or in
// `--no-interactive` mode.
package tui

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/sahilm/fuzzy"

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

// ----- huh-backed implementation -----

type HuhSelector struct{}

// SelectRepos uses an autocomplete-input loop instead of a multi-select
// over the full catalog. Showing every repo at once was overwhelming on
// orgs with hundreds of repos and provided no extra value over a typed
// query with tab-completion.
//
// Flow:
//   - LLM picks (if any) are pre-loaded into the selection.
//   - On each iteration the current selection is printed, then a one-line
//     huh.Input prompts the user to add a name (Tab to complete, blank
//     to finish, "-name" to drop one).
//   - The autocomplete suggestions are the full catalog.
//
// The loop exits when the user submits an empty input.
func (HuhSelector) SelectRepos(cat []catalog.Repo, picks []detector.RepoMatch) ([]string, error) {
	nameSet := make(map[string]bool, len(cat))
	names := make([]string, 0, len(cat))
	descByName := make(map[string]string, len(cat))
	for _, r := range cat {
		nameSet[r.Name] = true
		names = append(names, r.Name)
		descByName[r.Name] = r.Description
	}
	sort.Strings(names)

	selected := map[string]bool{}
	order := make([]string, 0, len(picks))
	for _, p := range picks {
		if nameSet[p.Name] && !selected[p.Name] {
			selected[p.Name] = true
			order = append(order, p.Name)
		}
	}

	if len(picks) == 0 {
		fmt.Println("\nLLM returned no suggestions — add repos manually below.")
		fmt.Printf("  catalog has %d repos · type to autocomplete · Tab to accept · blank to finish.\n", len(cat))
	} else {
		fmt.Printf("\nLLM suggested %d repo(s): %s\n", len(order), strings.Join(order, ", "))
		for _, p := range picks {
			if p.Reason != "" {
				fmt.Printf("  • %s (%.0f%%): %s\n", p.Name, p.Confidence*100, p.Reason)
			}
		}
	}

	for {
		printCurrentSelection(order)

		var input string
		err := huh.NewInput().
			Title("Add or drop a repo (blank to finish)").
			Description("type to filter · Tab to autocomplete · prefix \"-\" to drop").
			Suggestions(names).
			Value(&input).
			Run()
		if err != nil {
			return nil, err
		}
		input = strings.TrimSpace(input)
		if input == "" {
			break
		}
		// Drop intent: `-foo`
		if strings.HasPrefix(input, "-") {
			query := strings.TrimSpace(strings.TrimPrefix(input, "-"))
			match, err := resolveQuery(query, order, descByName)
			if err != nil {
				fmt.Printf("  ✗ %v\n", err)
				continue
			}
			delete(selected, match)
			order = removeFromSlice(order, match)
			fmt.Printf("  − dropped %s\n", match)
			continue
		}
		// Add intent: `foo` (exact, prefix, or fuzzy substring/subsequence).
		match, err := resolveQuery(input, names, descByName)
		if err != nil {
			fmt.Printf("  ✗ %v\n", err)
			continue
		}
		if selected[match] {
			fmt.Printf("  • %s is already in the selection\n", match)
			continue
		}
		selected[match] = true
		order = append(order, match)
		if d := descByName[match]; d != "" {
			fmt.Printf("  + %s — %s\n", match, truncate(d, 64))
		} else {
			fmt.Printf("  + %s\n", match)
		}
	}

	if len(order) == 0 {
		return nil, errors.New("no repos selected — nothing to do")
	}

	// Re-order to follow catalog order for predictability downstream.
	final := make([]string, 0, len(order))
	for _, r := range cat {
		if selected[r.Name] {
			final = append(final, r.Name)
		}
	}
	fmt.Printf("\n✓ Selected (%d): %s\n", len(final), strings.Join(final, ", "))
	return final, nil
}

func printCurrentSelection(order []string) {
	fmt.Println()
	if len(order) == 0 {
		fmt.Println("  current selection: (none)")
		return
	}
	fmt.Printf("  current selection (%d): %s\n", len(order), strings.Join(order, ", "))
}

// resolveQuery turns user-typed text into a concrete repo name.
//
// Resolution order:
//  1. exact match
//  2. unique prefix match
//  3. fuzzy subsequence match (e.g. "pymo" → "sentra-pymodels") — if a
//     single candidate is clearly best (score gap > 50%), pick it
//     directly; otherwise show a small disambiguation picker over the
//     top candidates.
//
// haystack is the searchable name list (full catalog when adding, the
// current selection when dropping). descByName is used only to enrich
// the disambiguation labels.
func resolveQuery(query string, haystack []string, descByName map[string]string) (string, error) {
	if query == "" {
		return "", errors.New("empty query")
	}
	// Exact
	for _, n := range haystack {
		if n == query {
			return n, nil
		}
	}
	// Unique prefix
	lq := strings.ToLower(query)
	var prefixMatches []string
	for _, n := range haystack {
		if strings.HasPrefix(strings.ToLower(n), lq) {
			prefixMatches = append(prefixMatches, n)
		}
	}
	if len(prefixMatches) == 1 {
		return prefixMatches[0], nil
	}
	// Fuzzy (subsequence) match — works on substrings, scoring by
	// character proximity.
	matches := fuzzy.Find(query, haystack)
	if len(matches) == 0 {
		return "", fmt.Errorf("%q: no match", query)
	}
	if len(matches) == 1 ||
		(len(matches) >= 2 && matches[0].Score >= matches[1].Score*3/2) {
		return matches[0].Str, nil
	}
	// Ambiguous — show the top candidates and let the user pick.
	const maxCandidates = 8
	opts := make([]huh.Option[string], 0, maxCandidates)
	for i, m := range matches {
		if i >= maxCandidates {
			break
		}
		label := m.Str
		if d := descByName[m.Str]; d != "" {
			label = fmt.Sprintf("%s — %s", m.Str, truncate(d, 56))
		}
		opts = append(opts, huh.NewOption(label, m.Str))
	}
	pick := matches[0].Str
	if err := huh.NewSelect[string]().
		Title(fmt.Sprintf("Multiple matches for %q — pick one", query)).
		Description("↑/↓ to move  ·  enter to confirm  ·  Esc to cancel this add").
		Options(opts...).
		Value(&pick).
		Run(); err != nil {
		return "", err
	}
	return pick, nil
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
