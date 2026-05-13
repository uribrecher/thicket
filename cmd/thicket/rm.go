package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

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

	// 1. Exact-slug short-circuit: preserves the old `thicket rm <slug>`
	//    behavior for muscle-memory + scripts.
	if len(args) == 1 {
		dir := filepath.Join(cfg.WorkspaceRoot, args[0])
		if _, err := os.Stat(dir); err == nil {
			return doRemove(cmd, dir, force)
		}
	}

	// 2. Otherwise enumerate managed workspaces and let the user pick.
	workspaces, err := listManagedWorkspaces(cfg)
	if err != nil {
		return err
	}
	if len(workspaces) == 0 {
		return errors.New("no workspaces to remove")
	}

	prefilter := ""
	if len(args) == 1 {
		prefilter = args[0]
	}
	picked, err := pickWorkspaceForRm(workspaces, prefilter)
	if err != nil {
		return err
	}
	if picked == nil {
		return nil
	}
	return doRemove(cmd, picked.path, force)
}

func doRemove(cmd *cobra.Command, dir string, force bool) error {
	w := workspace.New(git.New())
	if err := w.Remove(dir, force); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "removed %s\n", dir)
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
	for _, w := range workspaces {
		if w.slug == key {
			return &w, nil
		}
	}
	return nil, fmt.Errorf("picked workspace not found: %s", key)
}
