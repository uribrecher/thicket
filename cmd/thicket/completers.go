package main

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/uribrecher/thicket/internal/catalog"
	"github.com/uribrecher/thicket/internal/workspace"
)

// completeWorkspaceSlugs is the ValidArgsFunction for commands that take
// a single workspace slug (`rm`, `edit`). It returns the slugs of
// workspaces under workspace_root that have a thicket state manifest.
//
// Failures are intentionally silent: a missing/broken config or an
// unreadable workspace_root just yields no completions, never an error
// dialog in the shell.
func completeWorkspaceSlugs(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		// Positional slug already supplied — no further arg completion.
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	cfg, err := loadConfigOrPointAtInit()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	ws, _, err := workspace.ListManaged(cfg.WorkspaceRoot)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	out := make([]string, 0, len(ws))
	for _, w := range ws {
		// Decorate with the nickname (when set) as the cobra
		// description — shells that show descriptions (zsh, fish)
		// surface it; bash ignores it. Slug stays the actual
		// completed value. Tabs in the description would split the
		// line a second time and confuse cobra's parser, so we strip
		// them defensively — SanitizeNickname collapses tabs on new
		// writes, but legacy / hand-edited manifests are not
		// guaranteed clean.
		out = append(out, formatCompletion(w.Slug, w.State.Nickname))
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

// completeCatalogRepos completes `--only` repo names from the local
// catalog cache. `--only` is a comma-separated StringSlice, so the
// trailing token after the last comma is what we expand; already-typed
// repos are filtered out of the suggestion list.
//
// Cache miss / decode error → no completions, no error. Refreshing the
// cache from `gh` here would block the shell on an unbounded network
// call, which is the wrong tradeoff for tab completion.
func completeCatalogRepos(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	path, err := catalog.Path()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	repos, _, err := catalog.Load(path)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	prefix, partial := splitOnLastComma(toComplete)
	already := map[string]bool{}
	if prefix != "" {
		for _, p := range strings.Split(strings.TrimSuffix(prefix, ","), ",") {
			already[strings.TrimSpace(p)] = true
		}
	}

	out := make([]string, 0, len(repos))
	for _, r := range repos {
		if already[r.Name] {
			continue
		}
		if partial != "" && !strings.HasPrefix(r.Name, partial) {
			continue
		}
		// Mirror the slug+nickname pattern from completeWorkspaceSlugs:
		// emit the repo description as the cobra completion description
		// when it's non-empty. zsh/fish render it inline; bash ignores
		// it. The completion value (what actually lands on the command
		// line) is the prefix+name half — never the description.
		out = append(out, prefix+formatCompletion(r.Name, r.Description))
	}
	// NoSpace so the shell leaves the cursor flush against the
	// completed value — the user usually wants to type another `,foo`
	// right after, not a space.
	return out, cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveNoSpace
}

// formatCompletion joins a completion value and its human-facing
// description with a single tab — cobra's wire format for ValidArgs.
// Tabs and newlines inside the description would re-split the line and
// confuse the completion parser, so they're flattened to spaces before
// concatenation. An empty description yields the value alone.
func formatCompletion(value, desc string) string {
	if desc == "" {
		return value
	}
	clean := strings.NewReplacer("\t", " ", "\n", " ", "\r", " ").Replace(desc)
	return value + "\t" + clean
}

// splitOnLastComma divides a cobra `toComplete` value into the
// already-committed prefix (everything up to and including the last
// comma) and the partial token after it. `"a,b,c"` → `("a,b,", "c")`;
// `"foo"` → `("", "foo")`.
func splitOnLastComma(s string) (prefix, partial string) {
	i := strings.LastIndex(s, ",")
	if i < 0 {
		return "", s
	}
	return s[:i+1], s[i+1:]
}
