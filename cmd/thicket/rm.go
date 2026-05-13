package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

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

	workspaces, warnings, err := workspace.ListManaged(cfg.WorkspaceRoot)
	if err != nil {
		return err
	}
	for _, w := range warnings {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: %v\n", w)
	}

	if len(args) == 1 {
		if err := validateSlug(args[0]); err != nil {
			return err
		}
		slug := args[0]
		// Exact-slug short-circuit preserves the muscle-memory of
		// `thicket rm <full-slug>` for scripts. Falls through to the
		// picker (with the slug as prefilter) when the slug doesn't
		// match a managed workspace.
		for i := range workspaces {
			if workspaces[i].Slug == slug {
				return doRemove(cmd, workspaces[i].Path, &workspaces[i].State, force, skipConfirm)
			}
		}
		// Slug isn't in the managed list, but the directory might
		// still exist as an orphan (manifest deleted, partial create,
		// etc.). Honor `thicket rm <orphan-slug> --force` for those.
		// validateSlug above already locked the slug to a single
		// directory name under workspace_root.
		orphan := filepath.Join(cfg.WorkspaceRoot, slug)
		if info, statErr := os.Stat(orphan); statErr == nil && info.IsDir() {
			return doRemove(cmd, orphan, nil, force, skipConfirm)
		} else if statErr != nil && !errors.Is(statErr, fs.ErrNotExist) {
			return fmt.Errorf("stat %s: %w", orphan, statErr)
		}
		if len(workspaces) == 0 {
			return fmt.Errorf("no workspaces to remove (looked under %s)", cfg.WorkspaceRoot)
		}
		picked, err := pickWorkspaceForRm(workspaces, slug)
		if err != nil {
			return err
		}
		if picked == nil {
			return nil
		}
		return doRemove(cmd, picked.Path, &picked.State, force, skipConfirm)
	}

	if len(workspaces) == 0 {
		return errors.New("no workspaces to remove")
	}
	picked, err := pickWorkspaceForRm(workspaces, "")
	if err != nil {
		return err
	}
	if picked == nil {
		return nil
	}
	return doRemove(cmd, picked.Path, &picked.State, force, skipConfirm)
}

// doRemove prints a summary of what's about to be deleted, asks for
// confirmation (unless --yes), then runs workspace.RemoveWithState.
// st may be nil — that's the "no manifest" path (legacy/orphaned dir).
func doRemove(cmd *cobra.Command, dir string, st *workspace.State, force, skipConfirm bool) error {
	out := cmd.OutOrStdout()
	if !skipConfirm {
		printRemovePreview(out, dir, st, force)
		confirmed := false
		err := huh.NewConfirm().
			Title("Remove this workspace?").
			Description("This cannot be undone.").
			Affirmative("Yes, remove").
			Negative("No, keep it").
			Value(&confirmed).
			Run()
		// Ctrl+C / Esc through huh returns ErrUserAborted; treat
		// the same as "No, keep it" — friendly exit, not a hard
		// error. Mirrors the picker cancellation path.
		if errors.Is(err, huh.ErrUserAborted) {
			fmt.Fprintln(out, "cancelled.")
			return nil
		}
		if err != nil {
			return err
		}
		if !confirmed {
			fmt.Fprintln(out, "cancelled.")
			return nil
		}
	}

	w := workspace.New(git.New())
	if err := w.RemoveWithState(dir, st, force); err != nil {
		return err
	}
	fmt.Fprintf(out, "removed %s\n", dir)
	return nil
}

// printRemovePreview lays out exactly what `rm` is about to do so the
// user can review before saying yes — workspace dir, the worktrees
// inside it, and the cleanup semantics (force vs. preserve-on-dirty).
// st == nil signals "no manifest" so the user understands why no
// worktree details are shown.
func printRemovePreview(out io.Writer, dir string, st *workspace.State, force bool) {
	fmt.Fprintln(out)
	fmt.Fprintln(out, "About to remove this workspace:")
	fmt.Fprintf(out, "  path:    %s\n", dir)
	if st != nil {
		fmt.Fprintf(out, "  ticket:  %s\n", st.TicketID)
		fmt.Fprintf(out, "  branch:  %s\n", st.Branch)
		fmt.Fprintf(out, "  repos:   %d worktree(s)\n", len(st.Repos))
		for _, r := range st.Repos {
			fmt.Fprintf(out, "    • %s\n        worktree → %s\n        source   → %s\n",
				r.Name, r.WorktreePath, r.SourcePath)
		}
	} else if force {
		fmt.Fprintln(out, "  (no manifest — only the directory will be deleted)")
	} else {
		fmt.Fprintln(out, "  (no manifest — workspace.Remove will REFUSE this delete")
		fmt.Fprintln(out, "   without --force; current run will fail before doing any damage.)")
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

func pickWorkspaceForRm(workspaces []workspace.ManagedWorkspace, prefilter string) (*workspace.ManagedWorkspace, error) {
	columns := []tui.Column{
		{Title: "Slug", Width: 50},
		{Title: "Ticket", Width: 10},
		{Title: "Created", Width: 17},
		{Title: "Repos", Width: 5},
	}
	rows := make([]tui.Row, len(workspaces))
	for i, w := range workspaces {
		when := w.State.CreatedAt.Local().Format("2006-01-02 15:04")
		rows[i] = tui.Row{
			Key:    w.Slug,
			Cells:  []string{w.Slug, w.State.TicketID, when, fmt.Sprintf("%d", len(w.State.Repos))},
			Filter: w.Slug + " " + w.State.TicketID + " " + w.State.Branch,
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
		if workspaces[i].Slug == key {
			return &workspaces[i], nil
		}
	}
	return nil, fmt.Errorf("picked workspace not found: %s", key)
}
