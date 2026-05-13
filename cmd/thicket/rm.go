package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"github.com/uribrecher/thicket/internal/config"
	"github.com/uribrecher/thicket/internal/git"
	"github.com/uribrecher/thicket/internal/tui"
	"github.com/uribrecher/thicket/internal/workspace"
)

func runRm(cmd *cobra.Command, args []string) error {
	cfg, err := loadConfigOrPointAtInit()
	if err != nil {
		return err
	}
	force, _ := cmd.Flags().GetBool("force")
	skipConfirm, _ := cmd.Flags().GetBool("yes")

	// Exact-slug short-circuit preserves the muscle-memory of
	// `thicket rm <full-slug>` for scripts. Confirmation still applies
	// unless --yes is set.
	if len(args) == 1 {
		if err := validateSlug(args[0]); err != nil {
			return err
		}
		dir := filepath.Join(cfg.WorkspaceRoot, args[0])
		if _, err := os.Stat(dir); err == nil {
			return doRemove(cmd, dir, force, skipConfirm)
		}
	}


	workspaces, err := listManagedWorkspaces(cfg)
	if err != nil {
		return err
	}
	if len(workspaces) == 0 {
		return errors.New("no workspaces to remove")
	}

	prefilter := ""
	if len(args) == 1 {
		// validateSlug above already short-circuits absolute paths and
		// path-traversal attempts; here we just preserve whatever the
		// user typed as a search query.
		prefilter = args[0]
	}
	picked, err := pickWorkspaceForRm(workspaces, prefilter)
	if err != nil {
		return err
	}
	if picked == nil {
		return nil
	}
	return doRemove(cmd, picked.path, force, skipConfirm)
}

// doRemove prints a summary of what's about to be deleted, asks for
// confirmation (unless --yes), then runs workspace.Remove.
func doRemove(cmd *cobra.Command, dir string, force, skipConfirm bool) error {
	out := cmd.OutOrStdout()
	st, stateErr := workspace.ReadState(dir)

	if !skipConfirm {
		printRemovePreview(out, dir, st, stateErr, force)
		confirmed := false
		err := huh.NewConfirm().
			Title("Remove this workspace?").
			Description("This cannot be undone.").
			Affirmative("Yes, remove").
			Negative("No, keep it").
			Value(&confirmed).
			Run()
		if err != nil {
			return err
		}
		if !confirmed {
			fmt.Fprintln(out, "cancelled.")
			return nil
		}
	}

	w := workspace.New(git.New())
	if err := w.Remove(dir, force); err != nil {
		return err
	}
	fmt.Fprintf(out, "removed %s\n", dir)
	return nil
}

// printRemovePreview lays out exactly what `rm` is about to do so the
// user can review before saying yes — workspace dir, the worktrees
// inside it, and the cleanup semantics (force vs. preserve-on-dirty).
func printRemovePreview(out interface{ Write([]byte) (int, error) },
	dir string, st workspace.State, stateErr error, force bool) {

	fmt.Fprintln(out)
	fmt.Fprintln(out, "About to remove this workspace:")
	fmt.Fprintf(out, "  path:    %s\n", dir)
	if stateErr == nil {
		fmt.Fprintf(out, "  ticket:  %s\n", st.TicketID)
		fmt.Fprintf(out, "  branch:  %s\n", st.Branch)
		fmt.Fprintf(out, "  repos:   %d worktree(s)\n", len(st.Repos))
		for _, r := range st.Repos {
			fmt.Fprintf(out, "    • %s\n        worktree → %s\n        source   → %s\n",
				r.Name, r.WorktreePath, r.SourcePath)
		}
	} else {
		fmt.Fprintln(out, "  (no manifest — only the directory will be deleted)")
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Removal steps:")
	fmt.Fprintln(out, "  1. `git worktree remove` each worktree from its source repo")
	fmt.Fprintln(out, "     (the branch ref stays in the source repo — your work is")
	fmt.Fprintln(out, "      not lost as long as you've committed and/or pushed it).")
	fmt.Fprintln(out, "  2. Delete the workspace directory.")
	fmt.Fprintln(out)
	if force {
		fmt.Fprintln(out, "  ⚠ --force is set: dirty worktrees will be removed and their")
		fmt.Fprintln(out, "    uncommitted changes will be discarded.")
	} else {
		fmt.Fprintln(out, "  Without --force: any dirty worktree refuses removal and the")
		fmt.Fprintln(out, "  workspace directory is preserved (uncommitted work survives).")
	}
	fmt.Fprintln(out)
}

// validateSlug refuses values that would let filepath.Join escape
// workspace_root or land on something other than a simple directory
// name under it. Without this, `thicket rm /tmp` or `thicket rm ../..`
// could delete arbitrary directories.
func validateSlug(s string) error {
	if s == "" || s == "." || s == ".." {
		return fmt.Errorf("invalid slug %q", s)
	}
	if filepath.IsAbs(s) {
		return fmt.Errorf("invalid slug %q: must not be an absolute path", s)
	}
	if filepath.Clean(s) != s {
		return fmt.Errorf("invalid slug %q: contains path traversal or redundant separators", s)
	}
	if filepath.Base(s) != s {
		return fmt.Errorf("invalid slug %q: must be a single directory name with no separators", s)
	}
	return nil
}

// managedWorkspace is the slim projection rm + list need: the slug
// (folder name) plus enough metadata to render a useful picker row.
type managedWorkspace struct {
	slug   string
	path   string
	ticket string
	branch string
	when   string
	repos  int
}

func listManagedWorkspaces(cfg *config.Config) ([]managedWorkspace, error) {
	entries, err := os.ReadDir(cfg.WorkspaceRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", cfg.WorkspaceRoot, err)
	}
	var out []managedWorkspace
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		ws := filepath.Join(cfg.WorkspaceRoot, e.Name())
		st, err := workspace.ReadState(ws)
		if err != nil {
			// Not a thicket-managed workspace — ignore silently.
			continue
		}
		out = append(out, managedWorkspace{
			slug:   e.Name(),
			path:   ws,
			ticket: st.TicketID,
			branch: st.Branch,
			when:   st.CreatedAt.Local().Format("2006-01-02 15:04"),
			repos:  len(st.Repos),
		})
	}
	// Newest first — the most likely target for `thicket rm`.
	sort.Slice(out, func(i, j int) bool { return out[i].when > out[j].when })
	return out, nil
}

func pickWorkspaceForRm(workspaces []managedWorkspace, prefilter string) (*managedWorkspace, error) {
	columns := []tui.Column{
		{Title: "Slug", Width: 50},
		{Title: "Ticket", Width: 10},
		{Title: "Created", Width: 17},
		{Title: "Repos", Width: 5},
	}
	rows := make([]tui.Row, len(workspaces))
	for i, w := range workspaces {
		rows[i] = tui.Row{
			Key:    w.slug,
			Cells:  []string{w.slug, w.ticket, w.when, fmt.Sprintf("%d", w.repos)},
			Filter: w.slug + " " + w.ticket + " " + w.branch,
		}
	}
	key, err := tui.PickOne("Select a workspace to remove", columns, rows,
		tui.PickOneOption{InitialQuery: prefilter})
	if err != nil {
		if errors.Is(err, tui.ErrCancelled) {
			return nil, nil
		}
		return nil, err
	}
	for i := range workspaces {
		if workspaces[i].slug == key {
			return &workspaces[i], nil
		}
	}
	return nil, fmt.Errorf("picked workspace not found: %s", key)
}
